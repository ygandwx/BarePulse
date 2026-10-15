package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

type RedfishConfig struct {
	Network    NetworkPolicy
	Timeout    time.Duration
	SystemPath string
}
type Redfish struct {
	http       httpAdapter
	systemPath string
}

func NewRedfish(c RedfishConfig) *Redfish {
	return &Redfish{http: newHTTP(c.Network, c.Timeout), systemPath: c.SystemPath}
}

type redfishAction struct {
	Target string   `json:"target"`
	Values []string `json:"ResetType@Redfish.AllowableValues"`
	Info   string   `json:"@Redfish.ActionInfo"`
}
type redfishSystem struct {
	ID         string                   `json:"@odata.id"`
	PowerState string                   `json:"PowerState"`
	Actions    map[string]redfishAction `json:"Actions"`
}

func validSystemPath(path string) bool {
	if !strings.HasPrefix(path, "/redfish/v1/Systems/") {
		return false
	}
	name := strings.TrimPrefix(path, "/redfish/v1/Systems/")
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/%?#\\")
}

func validChildPath(path, parent string) bool {
	u, err := url.Parse(path)
	return err == nil && !u.IsAbs() && u.Host == "" && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && strings.HasPrefix(u.Path, parent+"/") && !strings.Contains(u.Path, "..") && !strings.Contains(u.Path, "\\")
}

func redfishError(data []byte) bool {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return true
	}
	_, exists := raw["error"]
	return exists
}

// DiscoverSystems returns candidate system identifiers for administrator binding.
// It deliberately does not select the first system or issue any reset.
func (d *Redfish) DiscoverSystems(ctx context.Context, t Target) ([]string, error) {
	if _, err := d.http.policy.validate(t.Address, false); err != nil {
		return nil, errors.New("device access disabled")
	}
	r, err := d.http.request(ctx, t, "GET", "/redfish/v1/Systems", nil, false)
	if err != nil || r.status != 200 || redfishError(r.data) {
		return nil, errors.New("system discovery failed")
	}
	var data struct {
		Members []struct {
			ID string `json:"@odata.id"`
		} `json:"Members"`
	}
	if json.Unmarshal(r.data, &data) != nil {
		return nil, errors.New("invalid system collection")
	}
	result := []string{}
	for _, member := range data.Members {
		if !validSystemPath(member.ID) {
			return nil, errors.New("invalid discovered system path")
		}
		result = append(result, member.ID)
	}
	return result, nil
}

func (d *Redfish) readSystem(ctx context.Context, t Target) (redfishSystem, Result, bool) {
	r, err := d.http.request(ctx, t, "GET", d.systemPath, nil, false)
	if err != nil || r.status != 200 {
		return redfishSystem{}, httpFailure(r, err, false), false
	}
	var system redfishSystem
	if redfishError(r.data) || json.Unmarshal(r.data, &system) != nil || system.ID != d.systemPath {
		return system, outcome("unknown", "STATE_UNTRUSTED", "Redfish system identity or response is invalid"), false
	}
	return system, Result{}, true
}

func (d *Redfish) Execute(ctx context.Context, t Target, action string) Result {
	if !validSystemPath(d.systemPath) {
		return outcome("failed", "SYSTEM_BINDING_REQUIRED", "An explicit discovered Redfish ComputerSystem path is required")
	}
	if _, err := d.http.policy.validate(t.Address, action != "status"); err != nil {
		return outcome("failed", "DEVICE_ACCESS_DISABLED", "Device network access or writes are disabled")
	}
	system, failure, ok := d.readSystem(ctx, t)
	if !ok {
		return failure
	}
	if action == "status" {
		return redfishState(system.PowerState)
	}
	reset, exists := system.Actions["#ComputerSystem.Reset"]
	if !exists || !validChildPath(reset.Target, d.systemPath) {
		return unsupported()
	}
	wanted := map[string]string{"power_on": "On", "request_shutdown": "GracefulShutdown", "hard_reset": "ForceRestart", "power_cycle": "PowerCycle", "hard_power_off": "ForceOff"}[action]
	if wanted == "" {
		return unsupported()
	}
	values := reset.Values
	if len(values) == 0 && validChildPath(reset.Info, d.systemPath) {
		r, err := d.http.request(ctx, t, "GET", reset.Info, nil, false)
		if err != nil || r.status != 200 {
			return httpFailure(r, err, false)
		}
		var info struct {
			Parameters []struct {
				Name   string   `json:"Name"`
				Values []string `json:"AllowableValues"`
			} `json:"Parameters"`
		}
		if redfishError(r.data) || json.Unmarshal(r.data, &info) != nil {
			return outcome("failed", "CAPABILITY_UNTRUSTED", "Reset ActionInfo is invalid")
		}
		for _, param := range info.Parameters {
			if param.Name == "ResetType" {
				values = param.Values
			}
		}
	}
	allowed := false
	for _, value := range values {
		if value == wanted {
			allowed = true
		}
	}
	if !allowed {
		return unsupported()
	}
	resp, err := d.http.request(ctx, t, "POST", reset.Target, map[string]string{"ResetType": wanted}, false)
	if err != nil || (resp.status != 200 && resp.status != 202 && resp.status != 204) {
		return httpFailure(resp, err, true)
	}
	if len(resp.data) > 0 && redfishError(resp.data) {
		r := unknown()
		r.Code = "DEVICE_RESPONSE_INVALID"
		return r
	}
	r := unknown()
	r.Code = "ACCEPTED_UNVERIFIED"
	r.Message = "Redfish accepted the action; physical completion has not been verified"
	r.Accepted = true
	// ON after reset/cycle cannot establish that a reboot occurred. TaskMonitor
	// support must later establish separate evidence; no automatic retry/fallback.
	if action == "hard_reset" || action == "power_cycle" {
		return r
	}
	after, _, ok := d.readSystem(ctx, t)
	if !ok {
		return r
	}
	state := redfishState(after.PowerState)
	r.PowerState = state.PowerState
	r.Quality = state.Quality
	r.ObservedAt = state.ObservedAt
	if (action == "power_on" && r.PowerState == "on") || ((action == "request_shutdown" || action == "hard_power_off") && r.PowerState == "off") {
		r.Status = "succeeded"
		r.Code = "TARGET_REACHED"
		r.Message = "The bound ComputerSystem reached its requested power state"
		r.Completed = true
		r.TargetReached = true
	}
	return r
}

func redfishState(power string) Result {
	r := outcome("succeeded", "STATE_OBSERVED", "ComputerSystem power state observed")
	r.Quality = "observed"
	r.Completed = true
	switch power {
	case "On":
		r.PowerState = "on"
	case "Off":
		r.PowerState = "off"
	case "PoweringOn":
		r.PowerState = "powering_on"
	case "PoweringOff":
		r.PowerState = "powering_off"
	default:
		r.Status = "unknown"
		r.Code = "STATE_UNKNOWN"
	}
	r.TargetReached = r.PowerState != "unknown"
	return r
}
