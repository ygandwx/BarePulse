package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type PiKVMConfig struct {
	Network         NetworkPolicy
	Timeout         time.Duration
	MappingVerified bool
}
type PiKVM struct {
	http            httpAdapter
	mappingVerified bool
}

func NewPiKVM(c PiKVMConfig) *PiKVM {
	return &PiKVM{http: newHTTP(c.Network, c.Timeout), mappingVerified: c.MappingVerified}
}

func envelope(data []byte) (map[string]json.RawMessage, error) {
	var env struct {
		OK     *bool                      `json:"ok"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &env) != nil || env.OK == nil || !*env.OK || env.Result == nil {
		return nil, errors.New("invalid device envelope")
	}
	if _, exists := env.Result["error"]; exists {
		return nil, errors.New("device reported an inner error")
	}
	return env.Result, nil
}

// ParseSelectedPort rejects the firmware's fallback 1 whenever error is present.
// chassis_status is intentionally ignored because the firmware hard-codes ON.
func ParseSelectedPort(data []byte) (int, error) {
	r, err := envelope(data)
	if err != nil {
		return 0, err
	}
	val, exists := r["kvm_status"]
	if !exists {
		return 0, errors.New("missing selected port")
	}
	var p int
	if json.Unmarshal(val, &p) != nil {
		var s string
		if json.Unmarshal(val, &s) != nil {
			return 0, errors.New("invalid selected port type")
		}
		s = strings.TrimSpace(s)
		if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, errors.New("selected port must be decimal")
		}
		p, err = strconv.Atoi(s)
		if err != nil {
			return 0, errors.New("invalid selected port")
		}
	}
	if p < 1 || p > 16 {
		return 0, errors.New("selected port out of range")
	}
	return p, nil
}

type PortState struct {
	PowerState string
	Connection string
	Enabled    bool
}

// ParseChassisType validates completeness but cannot recover port numbers lost
// by the firmware's row-order parser. MappingVerified is required before use.
func ParseChassisType(data []byte) (map[int]PortState, error) {
	r, err := envelope(data)
	if err != nil {
		return nil, err
	}
	var n int
	var raw map[string]string
	if json.Unmarshal(r["num"], &n) != nil || n != 16 || json.Unmarshal(r["res"], &raw) != nil || len(raw) != 16 {
		return nil, errors.New("incomplete port state")
	}
	result := make(map[int]PortState, 16)
	for p := 1; p <= 16; p++ {
		parts := strings.Split(raw[strconv.Itoa(p)], "|")
		if len(parts) != 3 {
			return nil, errors.New("malformed port row")
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		s := PortState{}
		switch parts[0] {
		case "ONL":
			s.Connection = "online"
		case "OFL":
			s.Connection = "offline"
		default:
			return nil, errors.New("unknown connection state")
		}
		switch parts[1] {
		case "ENB":
			s.Enabled = true
		case "DSB":
			s.Enabled = false
		default:
			return nil, errors.New("unknown enabled state")
		}
		switch parts[2] {
		case "ON":
			s.PowerState = "on"
		case "OFF":
			s.PowerState = "off"
		case "N/A":
			s.PowerState = "unknown"
		default:
			return nil, errors.New("unknown power state")
		}
		result[p] = s
	}
	return result, nil
}

func (d *PiKVM) Execute(ctx context.Context, t Target, action string) Result {
	if t.Port < 1 || t.Port > 16 {
		return outcome("failed", "INVALID_PORT", "PiKVM port must be an integer from 1 through 16")
	}
	if _, err := d.http.policy.validate(t.Address, action != "status"); err != nil {
		return outcome("failed", "DEVICE_ACCESS_DISABLED", "Device network access or writes are disabled")
	}
	if action == "status" {
		return d.readState(ctx, t)
	}
	paths := map[string]string{"power_on": "/api/open_node", "request_shutdown": "/api/close_node", "hard_reset": "/api/reset_node", "enable_port": "/api/enable_node", "disable_port": "/api/disable_node"}
	path, ok := paths[action]
	if !ok {
		return unsupported()
	}
	r := d.command(ctx, t, path)
	if action == "enable_port" || action == "disable_port" {
		// A timed-out maintenance command may still have executed. Cool down even
		// when acceptance is unknown. The caller persists this device-wide value.
		if r.Accepted || r.Status == "unknown" {
			r.CooldownSeconds = 30
		}
		return r
	}
	if !r.Accepted || action == "hard_reset" {
		return r
	}
	observed := d.readState(ctx, t)
	r.PowerState = observed.PowerState
	r.ObservedAt = observed.ObservedAt
	r.Quality = observed.Quality
	if (action == "power_on" && observed.PowerState == "on") || (action == "request_shutdown" && observed.PowerState == "off") {
		r.Status = "succeeded"
		r.Code = "TARGET_REACHED"
		r.Message = "The verified port reached the requested power state"
		r.Completed = true
		r.TargetReached = true
	}
	return r
}

func (d *PiKVM) command(ctx context.Context, t Target, path string) Result {
	resp, err := d.http.request(ctx, t, "POST", fmt.Sprintf("%s?num=%d", path, t.Port), nil, true)
	if err != nil || resp.status != 200 {
		return httpFailure(resp, err, true)
	}
	inner, err := envelope(resp.data)
	if err != nil {
		r := unknown()
		r.Code = "DEVICE_RESPONSE_INVALID"
		return r
	}
	var status int
	if json.Unmarshal(inner["status"], &status) != nil || string(inner["status"]) == "null" {
		r := unknown()
		r.Code = "DEVICE_STATUS_MISSING"
		return r
	}
	var output string
	if json.Unmarshal(inner["res"], &output) != nil || string(inner["res"]) == "null" {
		r := unknown()
		r.Code = "DEVICE_RESPONSE_INVALID"
		return r
	}
	if status != 0 {
		return outcome("failed", "DEVICE_COMMAND_FAILED", "Device command returned a nonzero exit status")
	}
	r := unknown()
	r.Accepted = true
	r.Code = "ACCEPTED_UNVERIFIED"
	r.Message = "Device command exited normally; physical completion has not been verified"
	return r
}

func (d *PiKVM) readState(ctx context.Context, t Target) Result {
	if !d.mappingVerified {
		return outcome("unknown", "PORT_MAPPING_UNVERIFIED", "Firmware row ordering has not been physically verified")
	}
	resp, err := d.http.request(ctx, t, "GET", "/api/chassis_type", nil, true)
	if err != nil || resp.status != 200 {
		return httpFailure(resp, err, false)
	}
	states, err := ParseChassisType(resp.data)
	if err != nil {
		return outcome("unknown", "STATE_UNTRUSTED", "Port state response is incomplete or invalid")
	}
	r := outcome("succeeded", "STATE_OBSERVED", "Observed a verified port mapping")
	r.PowerState = states[t.Port].PowerState
	r.Quality = "observed"
	r.Completed = true
	r.TargetReached = r.PowerState != "unknown"
	if !r.TargetReached {
		r.Status = "unknown"
		r.Code = "STATE_UNKNOWN"
	}
	return r
}

// SelectedPort is a scheduled read, not a customer endpoint. Its result alone
// never proves that old video, HID or media connections have been revoked.
func (d *PiKVM) SelectedPort(ctx context.Context, t Target) Result {
	if _, err := d.http.policy.validate(t.Address, false); err != nil {
		return outcome("failed", "DEVICE_ACCESS_DISABLED", "Device network access is disabled")
	}
	resp, err := d.http.request(ctx, t, "GET", "/api/chassis_status", nil, true)
	if err != nil || resp.status != 200 {
		return httpFailure(resp, err, false)
	}
	p, err := ParseSelectedPort(resp.data)
	if err != nil {
		return outcome("unknown", "SELECTED_PORT_UNKNOWN", "Selected target could not be verified")
	}
	r := outcome("succeeded", "STATE_OBSERVED", "Selected port was observed")
	r.SelectedPort = &p
	r.Quality = "observed"
	r.Completed = true
	r.TargetReached = true
	return r
}

// SelectPort requires the caller's durable whole-device console lease and prior
// verified revocation. It exposes no arbitrary device API or media endpoint.
func (d *PiKVM) SelectPort(ctx context.Context, t Target) Result {
	if t.Port < 1 || t.Port > 16 {
		return outcome("failed", "INVALID_PORT", "PiKVM port must be from 1 through 16")
	}
	if _, err := d.http.policy.validate(t.Address, true); err != nil {
		return outcome("failed", "DEVICE_ACCESS_DISABLED", "Device network access or writes are disabled")
	}
	r := d.command(ctx, t, "/api/kvm_node")
	if !r.Accepted {
		return r
	}
	s := d.SelectedPort(ctx, t)
	r.SelectedPort = s.SelectedPort
	r.Quality = s.Quality
	if s.SelectedPort != nil && *s.SelectedPort == t.Port {
		r.Status = "succeeded"
		r.Code = "TARGET_REACHED"
		r.Completed = true
		r.TargetReached = true
		r.Message = "Selected port verified; media isolation must be verified separately"
	}
	return r
}
