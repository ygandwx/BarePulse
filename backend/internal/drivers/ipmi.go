package drivers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const IPMIToolVersion = "1.8.19"

// IPMIConfig is server-owned authorization, never a customer request. The caller
// additionally validates server ownership/binding versions and holds the device
// lease. BinarySHA256 must identify an administrator-reviewed upstream build.
type IPMIConfig struct {
	AllowReal              bool
	AllowWrites            bool
	DeviceID               string
	Address                string
	Approved               bool
	Evidence               string
	AuthorizationExpiresAt time.Time
	AllowedActions         []string
	BinaryPath             string
	BinarySHA256           string
	ClientVersion          string
	UDPPort                uint16
	Privilege              string
	Timeout                time.Duration
}

type IPMICommandDriver struct {
	config IPMIConfig
	run    func(context.Context, string, []string, []string) ipmiCommandResult
}

type ipmiCommandResult struct {
	stdout, stderr string
	started        bool
	err            error
}

var ipmiCommands = map[string]struct{ command, acknowledgement string }{
	"status":           {"status", ""},
	"power_on":         {"on", "Chassis Power Control: Up/On"},
	"request_shutdown": {"soft", "Chassis Power Control: Soft"},
	"hard_reset":       {"reset", "Chassis Power Control: Reset"},
	"power_cycle":      {"cycle", "Chassis Power Control: Cycle"},
}

func NewIPMI(c IPMIConfig) (*IPMICommandDriver, error) {
	c.AllowedActions = slices.Clone(c.AllowedActions)
	if c.UDPPort == 0 {
		c.UDPPort = 623
	}
	if c.Privilege == "" {
		c.Privilege = "OPERATOR"
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
	}
	d := &IPMICommandDriver{config: c, run: runIPMICommand}
	if !c.AllowReal {
		return d, nil // Even construction does not probe an unapproved binary.
	}
	if !c.Approved || strings.TrimSpace(c.Evidence) == "" || !time.Now().Before(c.AuthorizationExpiresAt) || c.DeviceID == "" || net.ParseIP(c.Address) == nil {
		return nil, errors.New("IPMI requires current exact device authorization and a literal IP")
	}
	if c.ClientVersion != IPMIToolVersion || !filepath.IsAbs(c.BinaryPath) || filepath.Clean(c.BinaryPath) != c.BinaryPath || strings.ContainsAny(c.BinaryPath, "\x00\r\n") {
		return nil, errors.New("IPMI requires the reviewed ipmitool version and an absolute controlled binary path")
	}
	checksum, err := hex.DecodeString(c.BinarySHA256)
	if err != nil || len(checksum) != sha256.Size {
		return nil, errors.New("IPMI binary SHA256 is required")
	}
	d.config.BinarySHA256 = strings.ToLower(c.BinarySHA256)
	if (c.Privilege != "OPERATOR" && c.Privilege != "ADMINISTRATOR") || c.Timeout <= 0 || c.Timeout > time.Minute || len(c.AllowedActions) == 0 {
		return nil, errors.New("IPMI privilege, timeout or action approval is invalid")
	}
	for _, action := range c.AllowedActions {
		if _, ok := ipmiCommands[action]; !ok {
			return nil, errors.New("IPMI profile contains an unverified action")
		}
	}
	return d, nil
}

func (d *IPMICommandDriver) authorized(t Target, action string) bool {
	c := d.config
	return c.AllowReal && c.Approved && strings.TrimSpace(c.Evidence) != "" && time.Now().Before(c.AuthorizationExpiresAt) &&
		t.Kind == "ipmi" && t.DeviceID == c.DeviceID && t.Address == c.Address && net.ParseIP(t.Address) != nil && t.Port == 1 &&
		slices.Contains(c.AllowedActions, action) && (action == "status" || c.AllowWrites)
}

// Check the pinned file before each subprocess. Its directory and dependencies
// must remain administrator-owned; a hash is not protection against a privileged
// actor swapping executable files between verification and exec.
func (d *IPMICommandDriver) verifyBinary() error {
	info, err := os.Lstat(d.config.BinaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
		return errors.New("IPMI binary is not a controlled regular file")
	}
	if runtime.GOOS != "windows" && (info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0) {
		return errors.New("IPMI binary permissions are not restricted")
	}
	resolved, err := filepath.EvalSymlinks(d.config.BinaryPath)
	if err != nil || filepath.Clean(resolved) != d.config.BinaryPath {
		return errors.New("IPMI binary path must not traverse symlinks")
	}
	f, err := os.Open(d.config.BinaryPath)
	if err != nil {
		return errors.New("IPMI binary cannot be verified")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("IPMI binary changed while opening")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (64<<20)+1))
	if err != nil || n > 64<<20 || hex.EncodeToString(hash.Sum(nil)) != d.config.BinarySHA256 {
		return errors.New("IPMI binary checksum does not match approval")
	}
	return nil
}

func ipmiEnvironment(password string) []string {
	// No inherited PATH, LD_PRELOAD, IPMITOOL_PASSWORD, IPMI_KGKEY or arbitrary
	// application secrets. -E prefers IPMITOOL_PASSWORD if present, so omit it.
	env := []string{"LC_ALL=C", "LANG=C", "IPMI_PASSWORD=" + password}
	if runtime.GOOS == "windows" {
		if root := os.Getenv("SystemRoot"); root != "" {
			env = append(env, "SystemRoot="+root)
		}
	}
	return env
}

func (d *IPMICommandDriver) command(ctx context.Context, t Target, action, command string) ipmiCommandResult {
	if ctx.Err() != nil || !d.authorized(t, action) {
		return ipmiCommandResult{err: errors.New("IPMI authorization expired or execution cancelled")}
	}
	if err := d.verifyBinary(); err != nil {
		return ipmiCommandResult{err: err}
	}
	if ctx.Err() != nil || !d.authorized(t, action) {
		return ipmiCommandResult{err: errors.New("IPMI authorization expired or execution cancelled")}
	}
	// Upstream 1.8.19 uses try=0; while(try<retry). -R 1 sends the command
	// payload at most once. -R 0 silently restores the default (four attempts).
	// Session establishment may retry authentication packets, never this write.
	args := []string{"-I", "lanplus", "-C", "17", "-H", t.Address, "-p", strconv.Itoa(int(d.config.UDPPort)), "-U", t.Username, "-E", "-L", d.config.Privilege, "-R", "1", "-N", "2", "chassis", "power", command}
	return d.run(ctx, d.config.BinaryPath, args, ipmiEnvironment(t.Password))
}

func ipmiObserved(result ipmiCommandResult) (string, bool) {
	if result.err != nil || result.stderr != "" {
		return "unknown", false
	}
	switch strings.TrimSpace(result.stdout) {
	case "Chassis Power is on":
		return "on", true
	case "Chassis Power is off":
		return "off", true
	default:
		return "unknown", false
	}
}

func (d *IPMICommandDriver) Execute(ctx context.Context, t Target, action string) Result {
	command, ok := ipmiCommands[action]
	if !ok {
		return unsupported()
	}
	if !d.authorized(t, action) {
		return outcome("failed", "DEVICE_ACCESS_DISABLED", "IPMI device, action or current authorization is unavailable")
	}
	if len(t.Username) < 1 || len(t.Username) > 16 || len(t.Password) < 1 || len(t.Password) > 20 || strings.ContainsAny(t.Username+t.Password, "\x00\r\n") {
		return outcome("failed", "DEVICE_CREDENTIALS_INVALID", "IPMI requires valid bounded credentials from the server profile")
	}
	ctx, cancel := context.WithTimeout(ctx, d.config.Timeout)
	defer cancel()
	if err := d.verifyBinary(); err != nil || ctx.Err() != nil {
		return outcome("failed", "CLIENT_UNVERIFIED", "The approved IPMI client binary could not be verified")
	}
	version := d.run(ctx, d.config.BinaryPath, []string{"-V"}, ipmiEnvironment(""))
	if version.err != nil || version.stderr != "" || (strings.TrimSpace(version.stdout) != "ipmitool version "+IPMIToolVersion && strings.TrimSpace(version.stdout) != "ipmitool.exe version "+IPMIToolVersion) {
		return outcome("failed", "CLIENT_UNVERIFIED", "The IPMI client version does not match the reviewed retry contract")
	}
	before, observed := ipmiObserved(d.command(ctx, t, action, "status"))
	if !observed {
		return outcome("failed", "PRECHECK_UNAVAILABLE", "IPMI power state could not be verified; no power command was issued")
	}
	if action == "status" {
		r := outcome("succeeded", "STATE_OBSERVED", "Chassis power state observed; operating system boot is not verified")
		r.PowerState, r.Quality, r.Completed = before, "observed", true
		return r
	}
	wanted := ""
	if action == "power_on" {
		wanted = "on"
	} else if action == "request_shutdown" {
		wanted = "off"
	}
	if wanted != "" && before == wanted {
		r := outcome("succeeded", "ALREADY_AT_TARGET", "Chassis already has the requested power state; no power command issued")
		r.PowerState, r.Quality, r.Completed, r.TargetReached = before, "observed", true, true
		return r
	}
	if (action == "hard_reset" || action == "power_cycle") && before != "on" {
		return outcome("failed", "PRECONDITION_FAILED", "Reset and power cycle require verified chassis power ON")
	}
	written := d.command(ctx, t, action, command.command)
	if !written.started {
		return outcome("failed", "COMMAND_NOT_STARTED", "IPMI power command was not started")
	}
	r := unknown()
	if written.err != nil || written.stderr != "" || strings.TrimSpace(written.stdout) != command.acknowledgement {
		return r // Never reveal raw process output, and never retry an ambiguous write.
	}
	r.Accepted = true
	r.Code = "ACCEPTED_UNVERIFIED"
	r.Message = "IPMI acknowledged the command; physical completion has not been verified"
	if action == "hard_reset" || action == "power_cycle" {
		return r // A later ON value cannot prove a reset or power cycle occurred.
	}
	if after, ok := ipmiObserved(d.command(ctx, t, action, "status")); ok {
		r.PowerState, r.Quality = after, "observed"
		if after == wanted {
			r.Status, r.Code = "succeeded", "TARGET_REACHED"
			r.Message = "Chassis reached the requested power state; operating system boot or clean shutdown is not asserted"
			r.Completed, r.TargetReached = true, true
		}
	}
	return r
}

type ipmiOutput struct {
	data     bytes.Buffer
	overflow bool
}

func (b *ipmiOutput) Write(p []byte) (int, error) {
	const limit = 1 << 20
	n := len(p)
	remaining := limit - b.data.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.data.Write(p)
	return n, nil // Drain excess output without allocating unbounded memory.
}

func runIPMICommand(ctx context.Context, path string, args, env []string) ipmiCommandResult {
	cmd := exec.CommandContext(ctx, path, args...) // No shell and no PATH lookup.
	cmd.Env = env
	cmd.Dir = filepath.Dir(path)
	cmd.WaitDelay = time.Second
	var stdout, stderr ipmiOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return ipmiCommandResult{err: err}
	}
	err := cmd.Wait()
	if stdout.overflow || stderr.overflow {
		err = errors.New("IPMI output exceeded the bounded response limit")
	}
	return ipmiCommandResult{stdout: stdout.data.String(), stderr: stderr.data.String(), started: true, err: err}
}
