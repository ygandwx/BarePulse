package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func decodeTest[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
	var item T
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	return item
}
func seedFoundation(t *testing.T, a *App, admin, owner User, kind, scenario string) (Server, Device, Binding) {
	t.Helper()
	s := decodeTest[Server](t, testRequest(t, a, "POST", "/api/v1/servers", map[string]any{"name": "server-" + newID(), "owner_id": owner.ID}, admin), 201)
	d := decodeTest[Device](t, testRequest(t, a, "POST", "/api/v1/devices", map[string]any{"name": "device-" + newID(), "kind": kind, "scenario": scenario, "enabled": true}, admin), 201)
	b := decodeTest[Binding](t, testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s.ID, "device_id": d.ID, "kind": "power", "port": 1}, admin), 201)
	s = decodeTest[Server](t, testRequest(t, a, "GET", "/api/v1/servers/"+s.ID, nil, admin), 200)
	return s, d, b
}
func TestInventoryOwnershipConflictsAndHistory(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	alice, _, _ := testUser(t, a, "customer")
	bob, _, _ := testUser(t, a, "customer")
	s, d, b := seedFoundation(t, a, admin, alice, "mock_pikvm", "success")
	if w := testRequest(t, a, "GET", "/api/v1/servers/"+s.ID, nil, bob); w.Code != 404 {
		t.Fatalf("cross-owner server: %d", w.Code)
	}
	if w := testRequest(t, a, "POST", "/api/v1/devices", map[string]any{"name": "forbidden", "kind": "mock_pikvm"}, alice); w.Code != 403 {
		t.Fatalf("customer mutation: %d", w.Code)
	}
	s2 := decodeTest[Server](t, testRequest(t, a, "POST", "/api/v1/servers", map[string]any{"name": "second", "owner_id": bob.ID}, admin), 201)
	for _, kind := range []string{"power", "console"} {
		w := testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s2.ID, "device_id": d.ID, "kind": kind, "port": 1}, admin)
		if w.Code != 409 {
			t.Fatalf("physical port cross-binding %s: %d %s", kind, w.Code, w.Body)
		}
	}
	if w := testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s2.ID, "device_id": d.ID, "kind": "power", "port": 17}, admin); w.Code != 400 {
		t.Fatalf("invalid PiKVM port %d", w.Code)
	}
	replacement := decodeTest[Binding](t, testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s.ID, "device_id": d.ID, "kind": "power", "port": 2, "replace_id": b.ID, "version": b.Version}, admin), 201)
	if replacement.Version != 2 || replacement.Port != 2 {
		t.Fatalf("invalid replacement %+v", replacement)
	}
	bindings := decodeTest[[]Binding](t, testRequest(t, a, "GET", "/api/v1/bindings?server_id="+s.ID, nil, alice), 200)
	if len(bindings) != 2 {
		t.Fatalf("history lost: %+v", bindings)
	}
	var ended bool
	for _, v := range bindings {
		if v.ID == b.ID {
			ended = !v.Active && v.EndedAt != nil
		}
	}
	if !ended {
		t.Fatal("old binding not ended")
	}
	// Database uniqueness remains effective even outside the HTTP validator.
	_, err := a.DB.Exec(context.Background(), "INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES($1,$2,$3,'power',2)", newID(), s2.ID, d.ID)
	if err == nil {
		t.Fatal("database accepted a duplicate port")
	}
	_, err = a.DB.Exec(context.Background(), "INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES($1,$2,$3,'console',2)", newID(), s2.ID, d.ID)
	if err == nil {
		t.Fatal("database allowed different owners through power/console views of one physical port")
	}
}
func TestInventoryVersionMaintenanceAndLastAdministrator(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	customer, _, _ := testUser(t, a, "customer")
	s, _, _ := seedFoundation(t, a, admin, customer, "mock_bmc", "success")
	bad := testRequest(t, a, "PUT", "/api/v1/servers/"+s.ID, map[string]any{"name": s.Name, "owner_id": customer.ID, "version": s.Version - 1}, admin)
	if bad.Code != 409 {
		t.Fatalf("stale write %d %s", bad.Code, bad.Body)
	}
	good := decodeTest[Server](t, testRequest(t, a, "PUT", "/api/v1/servers/"+s.ID, map[string]any{"name": s.Name, "owner_id": customer.ID, "version": s.Version, "maintenance": true}, admin), 200)
	if !good.Maintenance || good.Version != s.Version+1 {
		t.Fatal("version/maintenance not updated")
	}
	response := testRequest(t, a, "PUT", "/api/v1/users/"+admin.ID, map[string]any{"email": admin.Email, "role": "customer"}, admin)
	if response.Code != 409 {
		t.Fatalf("last admin demoted: %d", response.Code)
	}
}
func TestBindingValidation(t *testing.T) {
	for _, tc := range []struct {
		kind, device string
		port         int
		ok           bool
	}{{"power", "mock_pikvm", 16, true}, {"power", "mock_pikvm", 17, false}, {"console", "mock_pdu", 1, false}, {"network", "snmp", 4094, true}, {"power", "mock_bmc", 2, false}, {"power", "mock_pdu", 1, true}, {"network", "snmp", 0, false}} {
		if got := validateBinding(tc.kind, tc.device, tc.port) == nil; got != tc.ok {
			t.Errorf("%+v got %v", tc, got)
		}
	}
}
