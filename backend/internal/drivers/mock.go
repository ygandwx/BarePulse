package drivers

import (
	"context"
	"fmt"
	"sync"
)

type mockState struct {
	power, supply string
	disabled      bool
}
type Mock struct {
	mu     sync.Mutex
	states map[string]mockState
}

func NewMock() *Mock { return &Mock{states: map[string]mockState{}} }

// Execute models local device state, not a real device. Durable task idempotency,
// cooling and uncertain-outcome fencing belong to the application scheduler.
func (m *Mock) Execute(ctx context.Context, t Target, action string) Result {
	if ctx.Err() != nil {
		return outcome("failed", "CANCELLED", "Cancelled before the simulated command was sent")
	}
	if t.Kind != "mock_pikvm" && t.Kind != "mock_pdu" && t.Kind != "mock_bmc" {
		return unsupported()
	}
	if t.DeviceID == "" || t.Port < 1 || (t.Kind == "mock_pikvm" && t.Port > 16) {
		return outcome("failed", "INVALID_PORT", "A valid server-side binding is required")
	}
	switch t.Scenario {
	case "", "success":
	case "offline":
		return outcome("failed", "DEVICE_OFFLINE", "Simulated device is offline")
	case "inner_error":
		return outcome("failed", "DEVICE_COMMAND_FAILED", "Simulated HTTP 200 response contains a nonzero device exit status")
	case "timeout":
		r := unknown()
		if t.Kind == "mock_pikvm" && (action == "enable_port" || action == "disable_port") {
			r.CooldownSeconds = 30
		}
		return r
	case "busy":
		return outcome("failed", "DEVICE_BUSY", "Simulated device is busy")
	default:
		return outcome("failed", "INVALID_SCENARIO", "Unknown simulation scenario")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states == nil {
		m.states = map[string]mockState{}
	}
	key := fmt.Sprintf("%s/%s/%d", t.Kind, t.DeviceID, t.Port)
	s, exists := m.states[key]
	if !exists {
		s = mockState{power: "off", supply: "off"}
	}
	r := outcome("succeeded", "SIMULATED", "Verified against local simulation state")
	r.Quality = "simulated"
	if t.Kind == "mock_pdu" {
		switch action {
		case "status":
		case "power_on":
			s.supply = "on"
		case "hard_power_off":
			s.supply = "off"
		case "power_cycle":
			r = unknown()
			r.Accepted = true
			r.Code = "ACCEPTED_UNVERIFIED"
			r.Quality = "simulated"
			r.SupplyState = s.supply
			return r
		default:
			return unsupported()
		}
		r.SupplyState = s.supply
	} else {
		switch action {
		case "status":
		case "power_on":
			if s.disabled {
				return outcome("failed", "PORT_DISABLED", "Enable this port before requesting power on")
			}
			s.power = "on"
		case "request_shutdown":
			s.power = "off"
		case "hard_reset", "power_cycle":
			if action == "power_cycle" && t.Kind != "mock_bmc" {
				return unsupported()
			}
			r = unknown()
			r.Accepted = true
			r.Code = "ACCEPTED_UNVERIFIED"
			r.Quality = "simulated"
			r.PowerState = s.power
			return r
		case "enable_port", "disable_port":
			if t.Kind != "mock_pikvm" {
				return unsupported()
			}
			s.disabled = action == "disable_port"
			if s.disabled {
				s.power = "off"
			}
			r.CooldownSeconds = 30
		default:
			return unsupported()
		}
		r.PowerState = s.power
	}
	m.states[key] = s
	r.Accepted = true
	r.Completed = true
	r.TargetReached = true
	return r
}
