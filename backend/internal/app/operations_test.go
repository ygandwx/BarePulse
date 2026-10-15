package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ygandwx/BarePulse/backend/internal/drivers"
)

func powerRequest(t *testing.T, a *App, u User, server, action, key string) *httptest.ResponseRecorder {
	t.Helper()
	token, csrf := newID(), newID()
	_, err := a.DB.Exec(context.Background(), "INSERT INTO auth_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)", tokenHash(token), u.ID, csrf, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"action": action})
	r := httptest.NewRequest("POST", "/api/v1/servers/"+server+"/power-actions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Idempotency-Key", key)
	r.AddCookie(&http.Cookie{Name: "bp_session", Value: token})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

type countingPower struct {
	calls   atomic.Int32
	result  drivers.Result
	entered chan struct{}
	release chan struct{}
}

func (d *countingPower) Execute(ctx context.Context, target drivers.Target, action string) drivers.Result {
	d.calls.Add(1)
	if d.entered != nil {
		d.entered <- struct{}{}
		select {
		case <-ctx.Done():
			return drivers.Result{Status: "unknown", Code: "TIMEOUT"}
		case <-d.release:
		}
	}
	return d.result
}
func successfulPower() drivers.Result {
	return drivers.Result{Status: "succeeded", Code: "MOCK_VERIFIED", Accepted: true, Completed: true, TargetReached: true, PowerState: "on"}
}

func TestPowerIdempotencyOwnershipAndExecution(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	other, _, _ := testUser(t, a, "customer")
	s, _, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", "success")
	if w := powerRequest(t, a, other, s.ID, "power_on", "unowned-key"); w.Code != 404 {
		t.Fatalf("cross-owner power %d", w.Code)
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- powerRequest(t, a, customer, s.ID, "power_on", "same-request-key")
		}()
	}
	wg.Wait()
	close(responses)
	ids := map[string]bool{}
	for w := range responses {
		if w.Code != 202 && w.Code != 200 {
			t.Fatalf("duplicate status %d %s", w.Code, w.Body)
		}
		var task OperationTask
		if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		ids[task.ID] = true
	}
	if len(ids) != 1 {
		t.Fatalf("duplicate tasks: %v", ids)
	}
	if w := powerRequest(t, a, customer, s.ID, "hard_reset", "same-request-key"); w.Code != 409 {
		t.Fatalf("idempotency payload mismatch %d", w.Code)
	}
	driver := &countingPower{result: successfulPower()}
	if worked, err := a.runOnePowerTask(context.Background(), driver); err != nil || !worked {
		t.Fatalf("worker %v %v", worked, err)
	}
	if worked, err := a.runOnePowerTask(context.Background(), driver); err != nil || worked {
		t.Fatalf("duplicate execution %v %v", worked, err)
	}
	if driver.calls.Load() != 1 {
		t.Fatal("command dispatched more than once")
	}
	for id := range ids {
		task := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+id, nil, customer), 200)
		if task.Status != "succeeded" {
			t.Fatalf("task %+v", task)
		}
		if w := testRequest(t, a, "GET", "/api/v1/tasks/"+id, nil, other); w.Code != 404 {
			t.Fatalf("cross-owner result %d", w.Code)
		}
	}
}
func TestPowerBindingChangeAndDeviceChangeRejectDispatch(t *testing.T) {
	for _, change := range []string{"binding", "owner", "device"} {
		t.Run(change, func(t *testing.T) {
			a := testApp(t)
			admin, _, _ := testUser(t, a, "admin")
			customer, _, _ := testUser(t, a, "customer")
			s, d, b := seedFoundation(t, a, admin, customer, "mock_pikvm", "success")
			task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "binding-drift-key"), 202)
			switch change {
			case "binding":
				decodeTest[Binding](t, testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s.ID, "device_id": d.ID, "kind": "power", "port": 2, "replace_id": b.ID, "version": b.Version}, admin), 201)
			case "owner":
				decodeTest[Server](t, testRequest(t, a, "PUT", "/api/v1/servers/"+s.ID, map[string]any{"name": s.Name, "owner_id": admin.ID, "version": s.Version}, admin), 200)
			case "device":
				decodeTest[Device](t, testRequest(t, a, "PUT", "/api/v1/devices/"+d.ID, map[string]any{"name": d.Name, "kind": d.Kind, "scenario": "success", "address": "changed.invalid", "enabled": true, "version": d.Version}, admin), 200)
			}
			driver := &countingPower{result: successfulPower()}
			if _, err := a.runOnePowerTask(context.Background(), driver); err != nil {
				t.Fatal(err)
			}
			if driver.calls.Load() != 0 {
				t.Fatal("stale target dispatched")
			}
			current := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, admin), 200)
			if current.Status != "failed" || !bytes.Contains(current.Result, []byte("BINDING_CHANGED")) {
				t.Fatalf("unexpected stale result %+v", current)
			}
		})
	}
}
func TestPowerUnknownBlocksAndRestartDoesNotReplay(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	s, d, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", "timeout")
	task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "unknown-request-key"), 202)
	driver := &countingPower{result: drivers.Result{Status: "unknown", Code: "OUTCOME_UNKNOWN", Accepted: true}}
	if _, err := a.runOnePowerTask(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverPowerTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runOnePowerTask(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	if driver.calls.Load() != 1 {
		t.Fatal("unknown command replayed")
	}
	var owner, blocked string
	if err := a.DB.QueryRow(context.Background(), "SELECT lease_owner,blocked_reason FROM devices WHERE id=$1", d.ID).Scan(&owner, &blocked); err != nil {
		t.Fatal(err)
	}
	if owner != task.ID || blocked == "" {
		t.Fatal("unknown lease released")
	}
	if w := powerRequest(t, a, customer, s.ID, "power_on", "new-unknown-key"); w.Code != 409 {
		t.Fatalf("blocked device accepted %d", w.Code)
	}
	if w := testRequest(t, a, "POST", "/api/v1/tasks/"+task.ID+"/resolve", map[string]any{"outcome": "failed", "reason": "offline test"}, admin); w.Code != 400 {
		t.Fatalf("missing executor stop evidence accepted %d", w.Code)
	}
	decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/tasks/"+task.ID+"/resolve", map[string]any{"outcome": "failed", "reason": "Mock execution stopped and result reconciled offline", "executor_stopped": true}, admin), 200)
	var lease *string
	if err := a.DB.QueryRow(context.Background(), "SELECT lease_owner FROM devices WHERE id=$1", d.ID).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	if lease != nil {
		t.Fatal("verified mock lease not cleared")
	}
}
func TestPowerRecoveryAndDeviceSerialization(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	s, d, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", "success")
	first := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "serialized-first"), 202)
	decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "request_shutdown", "serialized-second"), 202)
	driver := &countingPower{result: successfulPower(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := a.runOnePowerTask(context.Background(), driver); done <- err }()
	<-driver.entered
	if worked, err := a.runOnePowerTask(context.Background(), driver); err != nil || worked {
		t.Fatalf("overlapping worker dispatched: %v %v", worked, err)
	}
	if w := testRequest(t, a, "PUT", "/api/v1/servers/"+s.ID, map[string]any{"name": s.Name, "owner_id": admin.ID, "version": s.Version}, admin); w.Code != 409 {
		t.Fatalf("active server moved: %d", w.Code)
	}
	close(driver.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Emulate a crash after the transaction acquired its durable lease, before result persistence.
	if _, err := a.DB.Exec(context.Background(), "UPDATE operation_tasks SET status='running' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(context.Background(), "UPDATE devices SET lease_owner=$2,lease_kind='power' WHERE id=$1", d.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverPowerTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+first.ID, nil, admin), 200)
	if current.Status != "unknown" {
		t.Fatalf("crashed task reset to %s", current.Status)
	}
	if worked, err := a.runOnePowerTask(context.Background(), driver); err != nil || worked {
		t.Fatalf("unknown old executor taken over %v %v", worked, err)
	}
}
func TestPowerCooldownAndRealDevicesRemainDisabled(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	s, d, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", "success")
	if w := powerRequest(t, a, customer, s.ID, "disable_port", "customer-maintenance"); w.Code != 400 {
		t.Fatalf("customer maintenance action %d", w.Code)
	}
	decodeTest[OperationTask](t, powerRequest(t, a, admin, s.ID, "disable_port", "administrator-maintenance"), 202)
	driver := &countingPower{result: successfulPower()}
	start := time.Now()
	if _, err := a.runOnePowerTask(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	var until time.Time
	if err := a.DB.QueryRow(context.Background(), "SELECT cooldown_until FROM devices WHERE id=$1", d.ID).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if until.Sub(start) < 29*time.Second {
		t.Fatalf("short cooldown %v", until.Sub(start))
	}
	decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "wait-for-cooldown"), 202)
	if worked, err := a.runOnePowerTask(context.Background(), driver); err != nil || worked {
		t.Fatalf("cooldown ignored %v %v", worked, err)
	}
	real, _, _ := seedFoundation(t, a, admin, customer, "pikvm", "success")
	if w := powerRequest(t, a, customer, real.ID, "power_on", "real-devices-disabled"); w.Code != 409 {
		t.Fatalf("real device accepted: %d", w.Code)
	}
}
func TestPowerUnknownFieldsAndOperator(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	operator, _, _ := testUser(t, a, "operator")
	s, _, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", "success")
	if w := powerRequest(t, a, operator, s.ID, "power_on", "operator-request"); w.Code != 403 {
		t.Fatalf("operator write %d", w.Code)
	}
	if w := testRequest(t, a, "POST", "/api/v1/servers/"+s.ID+"/power-actions", map[string]any{"action": "power_on", "port": 2}, customer); w.Code != 400 {
		t.Fatalf("client supplied target accepted %d", w.Code)
	}
}
func TestPowerAcceptedIsNotCompletion(t *testing.T) {
	driver := &countingPower{result: drivers.Result{Status: "succeeded", Accepted: true, PowerState: "on"}}
	r := executePowerSafely(context.Background(), driver, drivers.Target{Kind: "mock_pikvm", Port: 1}, "hard_reset")
	if r.Status != "unknown" || r.Completed {
		t.Fatalf("acceptance was counted as completion %+v", r)
	}
}

func TestPowerMockScenariosAndSupplySemantics(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	driver := drivers.NewMock()
	for _, tc := range []struct{ scenario, status, code string }{
		{"success", "succeeded", "SIMULATED"},
		{"offline", "failed", "DEVICE_OFFLINE"},
		{"inner_error", "failed", "DEVICE_COMMAND_FAILED"},
		{"timeout", "unknown", "OUTCOME_UNKNOWN"},
		{"busy", "failed", "DEVICE_BUSY"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			s, _, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", tc.scenario)
			task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "mock-"+tc.scenario+"-request"), 202)
			if worked, err := a.runOnePowerTask(context.Background(), driver); err != nil || !worked {
				t.Fatalf("mock dispatch: %v %v", worked, err)
			}
			current := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, customer), 200)
			var result drivers.Result
			if err := json.Unmarshal(current.Result, &result); err != nil {
				t.Fatal(err)
			}
			if current.Status != tc.status || result.Code != tc.code {
				t.Fatalf("scenario %s produced %s %+v", tc.scenario, current.Status, result)
			}
		})
	}
	s, _, _ := seedFoundation(t, a, admin, customer, "mock_pdu", "success")
	task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "mock-pdu-supply"), 202)
	if _, err := a.runOnePowerTask(context.Background(), driver); err != nil {
		t.Fatal(err)
	}
	current := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, customer), 200)
	var result drivers.Result
	if err := json.Unmarshal(current.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.SupplyState != "on" || result.PowerState == "on" {
		t.Fatalf("PDU supply mistaken for server power: %+v", result)
	}
}

func testPowerProfile(target drivers.Target, serverID string) PowerProfile {
	return PowerProfile{DeviceID: target.DeviceID, Kind: target.Kind, Endpoint: target.Address, Approved: true, Evidence: "Offline contract fixture; not a physical-device authorization", AuthorizationExpiresAt: time.Now().Add(time.Hour), AllowedServerIDs: []string{serverID}, AllowedPorts: []int{target.Port}, AllowedActions: []string{"power_on"}, Username: "fixture-user", Password: "fixture-private-password", MappingVerified: true, RedfishSystemPath: "/redfish/v1/Systems/fixture"}
}
func TestRuntimePowerApprovalGuards(t *testing.T) {
	target := drivers.Target{DeviceID: "fixture-device", Kind: "pikvm", Address: "https://192.0.2.1", Port: 2}
	for _, tc := range []struct {
		name   string
		modify func(*App, *PowerProfile)
	}{
		{"reads_disabled", func(a *App, p *PowerProfile) { a.Config.AllowRealDevices = false }},
		{"writes_disabled", func(a *App, p *PowerProfile) { a.Config.AllowDeviceWrites = false }},
		{"unapproved", func(a *App, p *PowerProfile) { p.Approved = false }},
		{"expired", func(a *App, p *PowerProfile) { p.AuthorizationExpiresAt = time.Now().Add(-time.Second) }},
		{"wrong_device", func(a *App, p *PowerProfile) { p.DeviceID = "other" }},
		{"wrong_server", func(a *App, p *PowerProfile) { p.AllowedServerIDs = []string{"other"} }},
		{"wrong_port", func(a *App, p *PowerProfile) { p.AllowedPorts = []int{1} }},
		{"wrong_action", func(a *App, p *PowerProfile) { p.AllowedActions = []string{"hard_reset"} }},
		{"endpoint_changed", func(a *App, p *PowerProfile) { p.Endpoint = "https://192.0.2.2" }},
		{"unverified_mapping", func(a *App, p *PowerProfile) { p.MappingVerified = false }},
		{"missing_credentials", func(a *App, p *PowerProfile) { p.Password = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{Config: Config{AllowRealDevices: true, AllowDeviceWrites: true}}
			p := testPowerProfile(target, "fixture-server")
			tc.modify(a, &p)
			a.Config.PowerProfiles = map[string]PowerProfile{"fixture": p}
			if _, err := a.powerProfile(target, "fixture-server", "power_on", "fixture"); err == nil {
				t.Fatal("unapproved target accepted")
			}
		})
	}
	a := &App{Config: Config{AllowRealDevices: true, AllowDeviceWrites: true, PowerProfiles: map[string]PowerProfile{"fixture": testPowerProfile(target, "fixture-server")}}}
	p, err := a.powerProfile(target, "fixture-server", "power_on", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := configuredPowerDriver(p); err != nil {
		t.Fatal(err)
	} else if _, ok := d.(*drivers.PiKVM); !ok {
		t.Fatalf("wrong adapter %T", d)
	}
	if len(powerActions("ipmi", "operator")) != 0 || permitsPower("ipmi", "customer", "hard_power_off") {
		t.Fatal("IPMI exposed an unauthorized role or unsupported hard cut")
	}
}
func TestRuntimePowerProfileParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"fixture":{"device_id":"d","endpoint":"https://192.0.2.1","password":"do-not-echo","unknown_field":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPowerProfiles(path); err == nil || strings.Contains(err.Error(), "do-not-echo") {
		t.Fatal("invalid config accepted or secret echoed")
	}
	target := drivers.Target{DeviceID: "fixture-device", Kind: "pikvm", Address: "https://192.0.2.1", Port: 1}
	raw, err := json.Marshal(map[string]PowerProfile{"fixture": testPowerProfile(target, "fixture-server")})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadPowerProfiles(path)
	if err != nil || profiles["fixture"].Password != "fixture-private-password" {
		t.Fatalf("configuration did not load: %v", err)
	}
	if _, _, err := snmpPowerAlgorithms(&PowerSNMPProfile{AuthProtocol: "invented", PrivacyProtocol: "AES", AuthPassword: "fixture", PrivacyPassword: "fixture", DiscoveryOID: "1.3.6.1"}); err == nil {
		t.Fatal("unknown SNMP algorithm accepted")
	}
}

func TestRuntimePowerAPCFactoryDoesNotParseSNMPEndpointAsURL(t *testing.T) {
	p := PowerProfile{Kind: "apc", Endpoint: "192.0.2.1:161", Username: "fixture", APC: &drivers.APCProfile{}, SNMP: &PowerSNMPProfile{AuthProtocol: "SHA256", PrivacyProtocol: "AES", AuthPassword: "fixture-auth", PrivacyPassword: "fixture-privacy", DiscoveryOID: "1.3.6.1"}}
	driver, err := configuredPowerDriver(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*drivers.APC); !ok {
		t.Fatalf("wrong adapter %T", driver)
	}
}

type powerDriverFunc func(context.Context, drivers.Target, string) drivers.Result

func (f powerDriverFunc) Execute(ctx context.Context, target drivers.Target, action string) drivers.Result {
	return f(ctx, target, action)
}
func TestRuntimePowerApprovedLoopbackAndSecrets(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	s, d, _ := seedFoundation(t, a, admin, customer, "pikvm", "success")
	var commands atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-KVMD-User") != "fixture-user" || r.Header.Get("X-KVMD-Passwd") != "fixture-private-password" {
			t.Error("profile credentials were not injected server-side")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/open_node":
			if r.Method != "POST" || r.URL.Query().Get("num") != "1" {
				t.Error("target was not derived from binding")
			}
			commands.Add(1)
			jsonResponse(w, 200, map[string]any{"ok": true, "result": map[string]any{"status": 0, "res": "fixture command accepted"}})
		case "/api/chassis_type":
			ports := map[string]string{}
			for i := 1; i <= 16; i++ {
				ports[strconv.Itoa(i)] = "ONL |ENB |ON"
			}
			jsonResponse(w, 200, map[string]any{"ok": true, "result": map[string]any{"num": 16, "res": ports}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer endpoint.Close()
	// Production profiles require HTTPS. The internal test factory maps only this
	// owned loopback fixture to HTTP, without enabling insecure production access.
	approvedEndpoint := strings.Replace(endpoint.URL, "http://", "https://", 1)
	decodeTest[Device](t, testRequest(t, a, "PUT", "/api/v1/devices/"+d.ID, map[string]any{"name": d.Name, "kind": "pikvm", "address": approvedEndpoint, "credential_ref": "fixture-profile", "scenario": "success", "enabled": true, "version": d.Version}, admin), 200)
	a.Config.AllowRealDevices = true
	a.Config.AllowDeviceWrites = true
	a.Config.PowerProfiles = map[string]PowerProfile{"fixture-profile": testPowerProfile(drivers.Target{DeviceID: d.ID, Kind: "pikvm", Address: approvedEndpoint, Port: 1}, s.ID)}
	task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "approved-loopback-power"), 202)
	factory := func(p PowerProfile) (drivers.Driver, error) {
		inner := drivers.NewPiKVM(drivers.PiKVMConfig{Network: drivers.NetworkPolicy{AllowLoopback: true, AllowWrite: true}, MappingVerified: true})
		return powerDriverFunc(func(ctx context.Context, target drivers.Target, action string) drivers.Result {
			if target.Address != approvedEndpoint {
				t.Error("profile endpoint mismatch")
			}
			target.Address = endpoint.URL
			return inner.Execute(ctx, target, action)
		}), nil
	}
	if worked, err := a.runPowerTaskWithFactory(context.Background(), drivers.NewMock(), factory); err != nil || !worked {
		t.Fatalf("approved dispatch %v %v", worked, err)
	}
	result := testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, customer)
	current := decodeTest[OperationTask](t, result, 200)
	if current.Status != "succeeded" || commands.Load() != 1 {
		t.Fatalf("loopback result %s calls %d", current.Status, commands.Load())
	}
	for _, path := range []string{"/api/v1/tasks/" + task.ID, "/api/v1/audit", "/api/v1/servers/" + s.ID + "/power"} {
		response := testRequest(t, a, "GET", path, nil, customer)
		if strings.Contains(response.Body.String(), "fixture-private-password") || strings.Contains(response.Body.String(), approvedEndpoint) || strings.Contains(response.Body.String(), "fixture-user") {
			t.Fatalf("private device configuration exposed through %s", path)
		}
	}
	// Queued work must recheck approval after restart/configuration changes.
	second := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "revoked-approval-power"), 202)
	p := a.Config.PowerProfiles["fixture-profile"]
	p.Approved = false
	a.Config.PowerProfiles["fixture-profile"] = p
	if _, err := a.runPowerTaskWithFactory(context.Background(), drivers.NewMock(), factory); err != nil {
		t.Fatal(err)
	}
	current = decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+second.ID, nil, admin), 200)
	if current.Status != "failed" || commands.Load() != 1 {
		t.Fatal("revoked queued approval reached transport")
	}
}
func TestPowerInactiveServiceCannotExecute(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	s, _, _ := seedFoundation(t, a, admin, customer, "mock_pikvm", "success")
	task := decodeTest[OperationTask](t, powerRequest(t, a, customer, s.ID, "power_on", "before-service-suspended"), 202)
	ctx := context.Background()
	integrationID := newID()
	if _, err := a.DB.Exec(ctx, "INSERT INTO integrations(id,name,secret_ref) VALUES($1,$2,'fixture')", integrationID, "integration-"+integrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, "INSERT INTO service_mappings(id,integration_id,service_id,client_id,owner_id,server_id,state) VALUES($1,$2,'service','client',$3,$4,'suspended')", newID(), integrationID, customer.ID, s.ID); err != nil {
		t.Fatal(err)
	}
	if w := powerRequest(t, a, customer, s.ID, "power_on", "after-service-suspended"); w.Code != 409 {
		t.Fatalf("suspended service accepted %d %s", w.Code, w.Body)
	}
	driver := &countingPower{result: successfulPower()}
	if _, err := a.runOnePowerTask(ctx, driver); err != nil {
		t.Fatal(err)
	}
	current := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, admin), 200)
	if current.Status != "failed" || driver.calls.Load() != 0 || !bytes.Contains(current.Result, []byte("SERVICE_INACTIVE")) {
		t.Fatalf("suspended task dispatched %+v", current)
	}
}
