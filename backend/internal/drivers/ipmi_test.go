package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func ipmiFixture(t *testing.T) (IPMIConfig, Target) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ipmitool.exe")
	content := []byte("controlled offline fixture; never executed as ipmitool")
	if err := os.WriteFile(path, content, 0700); err != nil {
		t.Fatal(err)
	}
	// Windows TEMP may use an 8.3 alias; approvals use the canonical path.
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	c := IPMIConfig{AllowReal: true, AllowWrites: true, DeviceID: "fixture-device", Address: "192.0.2.45", Approved: true,
		Evidence: "offline runner contract only", AuthorizationExpiresAt: time.Now().Add(time.Hour),
		AllowedActions: []string{"status", "power_on", "request_shutdown", "hard_reset", "power_cycle"},
		BinaryPath:     path, BinarySHA256: hex.EncodeToString(hash[:]), ClientVersion: IPMIToolVersion}
	return c, Target{DeviceID: c.DeviceID, Kind: "ipmi", Address: c.Address, Port: 1, Username: "fixture-user", Password: "fixture-secret-123"}
}

func requireIPMI(t *testing.T, c IPMIConfig) *IPMICommandDriver {
	t.Helper()
	d, err := NewIPMI(c)
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowReal {
		if err := d.verifyBinary(); err != nil {
			t.Fatalf("fixture binary verification: %v", err)
		}
	}
	return d
}

func TestIPMIDefaultDenyAndExactApproval(t *testing.T) {
	c, target := ipmiFixture(t)
	denied := requireIPMI(t, IPMIConfig{})
	denied.run = func(context.Context, string, []string, []string) ipmiCommandResult {
		t.Fatal("default disabled driver started a process")
		return ipmiCommandResult{}
	}
	if got := denied.Execute(context.Background(), target, "status"); got.Status != "failed" || got.Code != "DEVICE_ACCESS_DISABLED" {
		t.Fatalf("default access not denied: %+v", got)
	}
	for _, change := range []string{"device", "address", "hostname", "port", "kind", "expiry", "write_gate", "action", "password", "username"} {
		t.Run(change, func(t *testing.T) {
			d, target := requireIPMI(t, c), target
			action := "power_on"
			switch change {
			case "device":
				target.DeviceID = "another-device"
			case "address":
				target.Address = "192.0.2.46"
			case "hostname":
				target.Address = "bmc.example.invalid"
			case "port":
				target.Port = 2
			case "kind":
				target.Kind = "redfish"
			case "expiry":
				d.config.AuthorizationExpiresAt = time.Now().Add(-time.Second)
			case "write_gate":
				d.config.AllowWrites = false
			case "action":
				action = "hard_power_off"
			case "password":
				target.Password = strings.Repeat("x", 21)
			case "username":
				target.Username = "invalid\nuser"
			}
			d.run = func(context.Context, string, []string, []string) ipmiCommandResult {
				t.Fatal("unapproved target started a process")
				return ipmiCommandResult{}
			}
			if got := d.Execute(context.Background(), target, action); got.Status != "failed" || got.Accepted {
				t.Fatalf("access not rejected: %+v", got)
			}
		})
	}
}

func TestIPMIConfigurationAndBinaryPin(t *testing.T) {
	c, target := ipmiFixture(t)
	for _, change := range []string{"relative_binary", "unknown_version", "missing_digest", "hostname", "unapproved", "unsupported_action", "privilege", "timeout"} {
		t.Run(change, func(t *testing.T) {
			invalid := c
			switch change {
			case "relative_binary":
				invalid.BinaryPath = "ipmitool"
			case "unknown_version":
				invalid.ClientVersion = "1.8.18"
			case "missing_digest":
				invalid.BinarySHA256 = ""
			case "hostname":
				invalid.Address = "not-a-literal.invalid"
			case "unapproved":
				invalid.Approved = false
			case "unsupported_action":
				invalid.AllowedActions = []string{"raw"}
			case "privilege":
				invalid.Privilege = "ADMINISTRATOR+"
			case "timeout":
				invalid.Timeout = time.Hour
			}
			if _, err := NewIPMI(invalid); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	d := requireIPMI(t, c)
	// Copy action approvals; callers cannot widen them after construction.
	c.AllowedActions[0] = "raw"
	if !slices.Contains(d.config.AllowedActions, "status") {
		t.Fatal("configuration slice alias retained")
	}
	if err := os.WriteFile(c.BinaryPath, []byte("changed executable"), 0700); err != nil {
		t.Fatal(err)
	}
	d.run = func(context.Context, string, []string, []string) ipmiCommandResult {
		t.Fatal("checksum mismatch started a process")
		return ipmiCommandResult{}
	}
	if got := d.Execute(context.Background(), target, "power_on"); got.Code != "CLIENT_UNVERIFIED" {
		t.Fatalf("binary pin not enforced: %+v", got)
	}
}

func TestIPMICommandContractAndStateSemantics(t *testing.T) {
	for _, tc := range []struct {
		action, before, after, wantStatus string
		wantWrites, wantReads             int
		accepted, completed               bool
	}{
		{"status", "on", "", "succeeded", 0, 1, false, true},
		{"power_on", "off", "on", "succeeded", 1, 2, true, true},
		{"request_shutdown", "on", "off", "succeeded", 1, 2, true, true},
		{"power_on", "on", "", "succeeded", 0, 1, false, true},
		{"request_shutdown", "off", "", "succeeded", 0, 1, false, true},
		{"hard_reset", "on", "on", "unknown", 1, 1, true, false},
		{"power_cycle", "on", "on", "unknown", 1, 1, true, false},
		{"power_cycle", "off", "", "failed", 0, 1, false, false},
	} {
		t.Run(tc.action+"_"+tc.before, func(t *testing.T) {
			c, target := ipmiFixture(t)
			d := requireIPMI(t, c)
			var reads, writes int
			d.run = func(_ context.Context, path string, args, env []string) ipmiCommandResult {
				if path != c.BinaryPath {
					t.Fatal("unapproved executable")
				}
				if slices.Equal(args, []string{"-V"}) {
					return ipmiCommandResult{started: true, stdout: "ipmitool version 1.8.19\n"}
				}
				prefix := []string{"-I", "lanplus", "-C", "17", "-H", target.Address, "-p", "623", "-U", target.Username, "-E", "-L", "OPERATOR", "-R", "1", "-N", "2", "chassis", "power"}
				if len(args) != len(prefix)+1 || !slices.Equal(args[:len(prefix)], prefix) || slices.Contains(args, target.Password) || slices.Contains(args, "-P") {
					t.Fatalf("unsafe command arguments: %v", args)
				}
				if !slices.Contains(env, "IPMI_PASSWORD="+target.Password) || slices.Contains(env, "IPMITOOL_PASSWORD=ambient-secret") {
					t.Fatal("password environment contract failed")
				}
				if args[len(args)-1] == "status" {
					reads++
					state := tc.before
					if reads > 1 {
						state = tc.after
					}
					return ipmiCommandResult{started: true, stdout: "Chassis Power is " + state + "\n"}
				}
				writes++
				if args[len(args)-1] != ipmiCommands[tc.action].command {
					t.Fatal("wrong physical command mapping")
				}
				return ipmiCommandResult{started: true, stdout: ipmiCommands[tc.action].acknowledgement + "\n"}
			}
			got := d.Execute(context.Background(), target, tc.action)
			if got.Status != tc.wantStatus || got.Accepted != tc.accepted || got.Completed != tc.completed || reads != tc.wantReads || writes != tc.wantWrites || got.SupplyState != "unknown" {
				t.Fatalf("state semantics: %+v reads=%d writes=%d", got, reads, writes)
			}
			if (tc.action == "hard_reset" || tc.action == "power_cycle") && got.TargetReached {
				t.Fatal("ON falsely proved reset completion")
			}
		})
	}
}

func TestIPMIAmbiguousWriteNeverRetriesOrLeaksOutput(t *testing.T) {
	for _, failure := range []string{"timeout", "nonzero", "wrong_ack", "stderr", "postcheck", "not_started"} {
		t.Run(failure, func(t *testing.T) {
			c, target := ipmiFixture(t)
			d := requireIPMI(t, c)
			writes, reads := 0, 0
			d.run = func(_ context.Context, _ string, args, _ []string) ipmiCommandResult {
				if slices.Equal(args, []string{"-V"}) {
					return ipmiCommandResult{started: true, stdout: "ipmitool version 1.8.19"}
				}
				if args[len(args)-1] == "status" {
					reads++
					if reads > 1 {
						return ipmiCommandResult{started: true, err: errors.New(target.Password)}
					}
					return ipmiCommandResult{started: true, stdout: "Chassis Power is off"}
				}
				writes++
				switch failure {
				case "timeout":
					return ipmiCommandResult{started: true, err: context.DeadlineExceeded}
				case "nonzero":
					return ipmiCommandResult{started: true, err: errors.New(target.Password), stdout: target.Password}
				case "wrong_ack":
					return ipmiCommandResult{started: true, stdout: "success " + target.Password}
				case "stderr":
					return ipmiCommandResult{started: true, stdout: "Chassis Power Control: Up/On", stderr: target.Password}
				case "not_started":
					return ipmiCommandResult{err: errors.New(target.Password)}
				default:
					return ipmiCommandResult{started: true, stdout: "Chassis Power Control: Up/On"}
				}
			}
			got := d.Execute(context.Background(), target, "power_on")
			want := "unknown"
			if failure == "not_started" {
				want = "failed"
			}
			if got.Status != want || writes != 1 || got.Completed || got.TargetReached || got.Accepted != (failure == "postcheck") {
				t.Fatalf("ambiguous write mishandled: %+v writes=%d", got, writes)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), target.Password) {
				t.Fatal("raw subprocess output leaked")
			}
		})
	}
}

func TestIPMIVersionPrecheckAndExpiryFence(t *testing.T) {
	for _, failure := range []string{"version", "precheck", "expiry"} {
		t.Run(failure, func(t *testing.T) {
			c, target := ipmiFixture(t)
			d := requireIPMI(t, c)
			calls := 0
			d.run = func(_ context.Context, _ string, args, _ []string) ipmiCommandResult {
				calls++
				if slices.Equal(args, []string{"-V"}) {
					if failure == "version" {
						return ipmiCommandResult{started: true, stdout: "ipmitool version 1.8.20"}
					}
					return ipmiCommandResult{started: true, stdout: "ipmitool version 1.8.19"}
				}
				if args[len(args)-1] != "status" {
					t.Fatal("failed prerequisite still sent write")
				}
				if failure == "expiry" {
					d.config.AuthorizationExpiresAt = time.Now().Add(-time.Second)
					return ipmiCommandResult{started: true, stdout: "Chassis Power is off"}
				}
				return ipmiCommandResult{started: true, stdout: "Chassis Power is maybe"}
			}
			got := d.Execute(context.Background(), target, "power_on")
			wantCode, wantCalls := "PRECHECK_UNAVAILABLE", 2
			if failure == "version" {
				wantCode, wantCalls = "CLIENT_UNVERIFIED", 1
			} else if failure == "expiry" {
				wantCode = "COMMAND_NOT_STARTED"
			}
			if got.Status != "failed" || got.Code != wantCode || got.Accepted || calls != wantCalls {
				t.Fatalf("prerequisite fence failed: %+v calls=%d", got, calls)
			}
		})
	}
}

func TestIPMIEnvironmentAndRealSubprocessBoundary(t *testing.T) {
	t.Setenv("IPMITOOL_PASSWORD", "ambient-secret")
	t.Setenv("LD_PRELOAD", "untrusted-library")
	for _, value := range ipmiEnvironment("fixture-password") {
		if strings.HasPrefix(value, "IPMITOOL_PASSWORD=") || strings.HasPrefix(value, "LD_PRELOAD=") || strings.HasPrefix(value, "PATH=") {
			t.Fatal("unsafe inherited environment")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"literal", "overflow", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if mode == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			}
			defer cancel()
			env := append(ipmiEnvironment("fixture-password"), "BAREPULSE_IPMI_HELPER=1", "BAREPULSE_IPMI_MODE="+mode)
			literal := "literal;$(echo should-not-run)"
			result := runIPMICommand(ctx, executable, []string{"-test.run=^TestIPMISubprocessHelper$", "--", literal}, env)
			if !result.started {
				t.Fatalf("fixture process not started: %v", result.err)
			}
			switch mode {
			case "literal":
				if result.err != nil || result.stdout != literal || result.stderr != "" {
					t.Fatalf("shell-free execution failed: %+v", result)
				}
			case "overflow":
				if result.err == nil || len(result.stdout) != 1<<20 {
					t.Fatal("unbounded subprocess response accepted")
				}
			case "timeout":
				if result.err == nil || ctx.Err() == nil {
					t.Fatal("subprocess did not honor execution deadline")
				}
			}
		})
	}
}

func TestIPMISubprocessHelper(t *testing.T) {
	if os.Getenv("BAREPULSE_IPMI_HELPER") != "1" {
		return
	}
	switch os.Getenv("BAREPULSE_IPMI_MODE") {
	case "literal":
		fmt.Print(os.Args[len(os.Args)-1])
	case "overflow":
		fmt.Print(strings.Repeat("x", (1<<20)+1))
	case "timeout":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}
