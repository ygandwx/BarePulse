// Package drivers implements device adapters. Callers must authorize a server,
// revalidate its binding, and hold the durable device lease before calling one.
package drivers

import (
	"context"
	"time"
)

type Target struct {
	DeviceID, Kind, Address, Username, Password, Scenario string
	Port                                                  int
}

type Result struct {
	Status          string    `json:"status"`
	Code            string    `json:"code"`
	Message         string    `json:"message"`
	PowerState      string    `json:"power_state"`
	SupplyState     string    `json:"supply_state"`
	Accepted        bool      `json:"accepted"`
	Completed       bool      `json:"completed"`
	TargetReached   bool      `json:"target_reached"`
	CooldownSeconds int       `json:"cooldown_seconds"`
	ObservedAt      time.Time `json:"observed_at"`
	Quality         string    `json:"quality"`
	SelectedPort    *int      `json:"selected_port,omitempty"`
}

type Driver interface {
	Execute(context.Context, Target, string) Result
}

func outcome(status, code, message string) Result {
	return Result{Status: status, Code: code, Message: message, PowerState: "unknown", SupplyState: "unknown", Quality: "unknown", ObservedAt: time.Now().UTC()}
}
func unsupported() Result {
	return outcome("failed", "CAPABILITY_UNSUPPORTED", "This driver has no verified capability for the requested action")
}
func unknown() Result {
	return outcome("unknown", "OUTCOME_UNKNOWN", "The command outcome is unknown; do not resend automatically")
}

// IPMI deliberately makes no network call. No particular BMC firmware or IPMI
// command capability was verified for the installation. Use confirmed Redfish
// capabilities where available; never silently fall back from another driver.
type IPMI struct{}

func (IPMI) Execute(context.Context, Target, string) Result { return unsupported() }
