package drivers

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// APCProfile is an administrator-provided, firmware-specific confirmation record.
// There are intentionally no built-in AP8xxx/AP7921B OIDs or command enums.
type APCProfile struct {
	Confirmed bool              `json:"confirmed"`
	Model     string            `json:"model"`
	Firmware  string            `json:"firmware"`
	Evidence  string            `json:"evidence"`
	Outlets   map[int]APCOutlet `json:"outlets"`
}
type APCOutlet struct {
	StateOID    string         `json:"state_oid"`
	ControlOID  string         `json:"control_oid"`
	StateValues map[int]string `json:"state_values"` // Only "on" and "off" are understood.
	Actions     map[string]int `json:"actions"`      // Explicit integer values from the confirmed MIB.
}

// SNMPIntegerTransport performs exactly one read/write transaction. Implementors
// must never retry SET or hide a transport error after a possibly-sent request.
type SNMPIntegerTransport interface {
	GetInteger(context.Context, Target, string) (int, error)
	SetInteger(context.Context, Target, string, int) error
}
type APC struct {
	Profile                 APCProfile
	Transport               SNMPIntegerTransport
	AllowReads, AllowWrites bool
}

func validOID(oid string) bool {
	parts := strings.Split(strings.TrimPrefix(oid, "."), ".")
	if len(parts) < 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return false
		}
		if _, err := strconv.ParseUint(p, 10, 32); err != nil {
			return false
		}
	}
	if parts[0] != "0" && parts[0] != "1" && parts[0] != "2" {
		return false
	}
	second, _ := strconv.ParseUint(parts[1], 10, 32)
	return parts[0] == "2" || second <= 39
}
func (d *APC) outlet(port int) (APCOutlet, error) {
	if !d.Profile.Confirmed || d.Profile.Model == "" || d.Profile.Firmware == "" || d.Profile.Evidence == "" {
		return APCOutlet{}, errors.New("confirmed firmware profile required")
	}
	o, ok := d.Profile.Outlets[port]
	if !ok || port < 1 || !validOID(o.StateOID) || !validOID(o.ControlOID) || len(o.StateValues) == 0 {
		return APCOutlet{}, errors.New("confirmed outlet mapping required")
	}
	for _, v := range o.StateValues {
		if v != "on" && v != "off" {
			return APCOutlet{}, errors.New("invalid supply state mapping")
		}
	}
	return o, nil
}

func (d *APC) Execute(ctx context.Context, t Target, action string) Result {
	if !d.AllowReads || d.Transport == nil || (action != "status" && !d.AllowWrites) {
		return outcome("failed", "DEVICE_ACCESS_DISABLED", "APC read access or writes are disabled")
	}
	o, err := d.outlet(t.Port)
	if err != nil {
		return outcome("failed", "CAPABILITY_UNVERIFIED", "A confirmed model, firmware, outlet OIDs and enum profile is required")
	}
	if action == "status" {
		return d.readState(ctx, t, o)
	}
	// Never translate graceful shutdown into a hard outlet cut.
	if action != "power_on" && action != "hard_power_off" && action != "power_cycle" {
		return unsupported()
	}
	value, exists := o.Actions[action]
	if !exists {
		return unsupported()
	}
	if err := d.Transport.SetInteger(ctx, t, o.ControlOID, value); err != nil {
		return unknown()
	}
	r := unknown()
	r.Accepted = true
	r.Code = "ACCEPTED_UNVERIFIED"
	r.Message = "PDU accepted the outlet command; server boot is not implied"
	if action == "power_cycle" {
		return r
	}
	s := d.readState(ctx, t, o)
	r.SupplyState = s.SupplyState
	r.Quality = s.Quality
	r.ObservedAt = s.ObservedAt
	if (action == "power_on" && s.SupplyState == "on") || (action == "hard_power_off" && s.SupplyState == "off") {
		r.Status = "succeeded"
		r.Code = "SUPPLY_TARGET_REACHED"
		r.Message = "Outlet supply state verified; server power and boot state remain unknown"
		r.Completed = true
		r.TargetReached = true
	}
	return r
}
func (d *APC) readState(ctx context.Context, t Target, o APCOutlet) Result {
	value, err := d.Transport.GetInteger(ctx, t, o.StateOID)
	if err != nil {
		return outcome("unknown", "STATE_UNAVAILABLE", "PDU supply state could not be read")
	}
	state, ok := o.StateValues[value]
	if !ok {
		return outcome("unknown", "STATE_UNTRUSTED", "PDU returned an unrecognized outlet state")
	}
	r := outcome("succeeded", "SUPPLY_STATE_OBSERVED", "Outlet supply state observed; server power remains unknown")
	r.SupplyState = state
	r.Quality = "observed"
	r.Completed = true
	r.TargetReached = true
	return r
}
