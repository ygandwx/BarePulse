package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ygandwx/BarePulse/backend/internal/drivers"
)

func ipmiPowerProfile(t *testing.T, target drivers.Target, serverID string) PowerProfile {
	t.Helper()
	p := testPowerProfile(target, serverID)
	p.Password = "fixture-secret-123"
	p.IPMI = &PowerIPMIProfile{BinaryPath: filepath.Join(t.TempDir(), "not-installed-ipmitool"), BinarySHA256: strings.Repeat("0", 64), ClientVersion: drivers.IPMIToolVersion}
	return p
}

func TestRuntimeIPMIProfileGuards(t *testing.T) {
	target := drivers.Target{DeviceID: "offline-ipmi", Kind: "ipmi", Address: "192.0.2.45", Port: 1}
	for _, tc := range []struct {
		name   string
		change func(*App, *PowerProfile, *drivers.Target)
	}{
		{"reads_disabled", func(a *App, _ *PowerProfile, _ *drivers.Target) { a.Config.AllowRealDevices = false }},
		{"writes_disabled", func(a *App, _ *PowerProfile, _ *drivers.Target) { a.Config.AllowDeviceWrites = false }},
		{"no_client", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.IPMI = nil }},
		{"wrong_version", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.IPMI.ClientVersion = "unreviewed" }},
		{"no_checksum", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.IPMI.BinarySHA256 = "" }},
		{"relative_client", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.IPMI.BinaryPath = "ipmitool" }},
		{"bad_privilege", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.IPMI.Privilege = "CUSTOM" }},
		{"wrong_bmc_port", func(_ *App, p *PowerProfile, target *drivers.Target) { p.AllowedPorts = []int{2}; target.Port = 2 }},
		{"hostname", func(_ *App, p *PowerProfile, target *drivers.Target) {
			p.Endpoint = "bmc.example.test"
			target.Address = p.Endpoint
		}},
		{"expired", func(_ *App, p *PowerProfile, _ *drivers.Target) {
			p.AuthorizationExpiresAt = time.Now().Add(-time.Second)
		}},
		{"other_owner_scope", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.AllowedServerIDs = []string{"other-server"} }},
		{"credential_overflow", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.Password = strings.Repeat("x", 21) }},
		{"credential_newline", func(_ *App, p *PowerProfile, _ *drivers.Target) { p.Username = "bad\nuser" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{Config: Config{AllowRealDevices: true, AllowDeviceWrites: true}}
			p, target := ipmiPowerProfile(t, target, "server"), target
			tc.change(a, &p, &target)
			a.Config.PowerProfiles = map[string]PowerProfile{"fixture": p}
			if _, err := a.powerProfile(target, "server", "power_on", "fixture"); err == nil {
				t.Fatal("invalid IPMI authorization accepted")
			}
		})
	}
	p := ipmiPowerProfile(t, target, "server")
	a := &App{Config: Config{AllowRealDevices: true, AllowDeviceWrites: true, PowerProfiles: map[string]PowerProfile{"fixture": p}}}
	if _, err := a.powerProfile(target, "server", "power_on", "fixture"); err != nil {
		t.Fatal(err)
	}
	d, err := configuredPowerDriver(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(*drivers.IPMICommandDriver); !ok {
		t.Fatalf("unexpected adapter %T", d)
	}
	target.Username, target.Password = p.Username, p.Password
	// There is deliberately no executable at this path. No subprocess or device
	// call can occur; a structurally valid profile does not bypass integrity checks.
	if result := d.Execute(context.Background(), target, "power_on"); result.Code != "CLIENT_UNVERIFIED" || result.Status != "failed" {
		t.Fatalf("missing approved client did not fail closed: %+v", result)
	}
}

func TestRuntimeIPMIDurableDispatchAndRevocation(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	other, _, _ := testUser(t, a, "customer")
	s, d, _ := seedFoundation(t, a, admin, customer, "ipmi", "success")
	endpoint := "192.0.2.45"
	decodeTest[Device](t, testRequest(t, a, "PUT", "/api/v1/devices/"+d.ID, map[string]any{"name": d.Name, "kind": "ipmi", "address": endpoint, "credential_ref": "fixture-ipmi", "scenario": "success", "enabled": true, "version": d.Version}, admin), 200)
	if w := powerRequest(t, a, customer, s.ID, "power_on", "ipmi-default-denied"); w.Code != 409 || !strings.Contains(w.Body.String(), "REAL_DEVICES_DISABLED") {
		t.Fatalf("default IPMI boundary bypassed: %d %s", w.Code, w.Body)
	}
	a.Config.AllowRealDevices, a.Config.AllowDeviceWrites = true, true
	p := ipmiPowerProfile(t, drivers.Target{DeviceID: d.ID, Kind: "ipmi", Address: endpoint, Port: 1}, s.ID)
	a.Config.PowerProfiles = map[string]PowerProfile{"fixture-ipmi": p}
	if w := powerRequest(t, a, other, s.ID, "power_on", "ipmi-other-customer"); w.Code != 404 {
		t.Fatalf("cross-customer IPMI access: %d", w.Code)
	}
	var calls int
	factory := func(profile PowerProfile) (drivers.Driver, error) {
		return powerDriverFunc(func(_ context.Context, target drivers.Target, action string) drivers.Result {
			calls++
			if target.DeviceID != d.ID || target.Address != endpoint || target.Port != 1 || target.Username != p.Username || target.Password != p.Password || action != "power_on" || profile.IPMI == nil {
				t.Error("IPMI target did not come from approved binding and secret profile")
			}
			return drivers.Result{Status: "unknown", Code: "OUTCOME_UNKNOWN", Message: "Offline transport uncertainty fixture"}
		}), nil
	}
	// Queued requests must revalidate the profile before a driver is called.
	revoked := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "ipmi-revoke-queued"), 202)
	p.Approved = false
	a.Config.PowerProfiles["fixture-ipmi"] = p
	if worked, err := a.runPowerTaskWithFactory(context.Background(), drivers.NewMock(), factory); err != nil || !worked {
		t.Fatalf("revocation dispatch: %v %v", worked, err)
	}
	current := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+revoked.ID, nil, customer), 200)
	if current.Status != "failed" || calls != 0 {
		t.Fatal("revoked IPMI reached driver")
	}
	p.Approved = true
	a.Config.PowerProfiles["fixture-ipmi"] = p
	task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "ipmi-ambiguous-write"), 202)
	if worked, err := a.runPowerTaskWithFactory(context.Background(), drivers.NewMock(), factory); err != nil || !worked {
		t.Fatalf("IPMI dispatch: %v %v", worked, err)
	}
	current = decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, customer), 200)
	if current.Status != "unknown" || calls != 1 {
		t.Fatalf("uncertain IPMI outcome: %s calls=%d", current.Status, calls)
	}
	again := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "ipmi-ambiguous-write"), 200)
	if again.ID != task.ID || again.Status != "unknown" {
		t.Fatal("idempotent retry replaced uncertain task")
	}
	if w := powerRequest(t, a, customer, s.ID, "power_on", "ipmi-new-blocked-key"); w.Code != 409 || !strings.Contains(w.Body.String(), "DEVICE_BLOCKED") {
		t.Fatalf("uncertain device accepted another write: %d %s", w.Code, w.Body)
	}
	if worked, err := a.runPowerTaskWithFactory(context.Background(), drivers.NewMock(), factory); err != nil || worked || calls != 1 {
		t.Fatalf("uncertain task retried: %v %v %d", worked, err, calls)
	}
	for _, path := range []string{"/api/v1/tasks/" + task.ID, "/api/v1/audit", "/api/v1/servers/" + s.ID + "/power"} {
		response := testRequest(t, a, "GET", path, nil, customer)
		if response.Code != 200 {
			t.Fatalf("read response %d", response.Code)
		}
		for _, secret := range []string{p.Username, p.Password, endpoint, p.IPMI.BinaryPath, p.IPMI.BinarySHA256} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("private IPMI profile exposed through %s", path)
			}
		}
	}
	var summary struct {
		Actions          []string `json:"actions"`
		ExecutionEnabled bool     `json:"execution_enabled"`
	}
	w := testRequest(t, a, "GET", "/api/v1/servers/"+s.ID+"/power", nil, customer)
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Actions) != 1 || summary.Actions[0] != "power_on" || summary.ExecutionEnabled {
		t.Fatalf("UI capabilities ignored exact profile or unknown fence: %+v", summary)
	}
}
