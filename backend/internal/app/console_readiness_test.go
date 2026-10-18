package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestConsoleReadinessIsScopedAndNeverProbesDevices(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	other, _, _ := testUser(t, a, "customer")
	s, d := seedConsole(t, a, admin, u, "success")
	path := "/api/v1/servers/" + s.ID + "/console-readiness"
	read := func() ConsoleReadiness {
		return decodeTest[ConsoleReadiness](t, testRequest(t, a, "GET", path, nil, u), 200)
	}
	v := read()
	if !v.RequestAllowed || v.Mode != "simulated" || len(v.Channels) != 3 || v.QueueExpected {
		t.Fatalf("bad simulation preflight %+v", v)
	}
	if got := testRequest(t, a, "GET", path, nil, other); got.Code != 404 {
		t.Fatalf("cross-customer preflight %d", got.Code)
	}
	c := requestConsoleTest(t, a, u, s.ID)
	if !read().QueueExpected {
		t.Fatal("occupied device was reported immediately available")
	}
	decodeTest[ConsoleSession](t, testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID, nil, u), 202)
	var requests atomic.Int32
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); t.Error("readiness contacted a device") }))
	defer device.Close()
	if _, err := a.DB.Exec(context.Background(), `UPDATE devices SET kind='pikvm',address=$2,credential_ref='private-reference' WHERE id=$1`, d.ID, device.URL); err != nil {
		t.Fatal(err)
	}
	// Even deployment switches cannot make this metadata endpoint probe hardware
	// or grant access to a real channel for which the adapter is not implemented.
	a.Config.AllowRealDevices = true
	a.Config.AllowDeviceWrites = true
	w := testRequest(t, a, "GET", path, nil, u)
	v = decodeTest[ConsoleReadiness](t, w, 200)
	if v.RequestAllowed || v.Mode != "real" || v.Code != "ISOLATION_UNVERIFIED" || len(v.Channels) != 0 {
		t.Fatalf("real access misrepresented %+v", v)
	}
	if strings.Contains(w.Body.String(), device.URL) || strings.Contains(w.Body.String(), "private-reference") {
		t.Fatal("private device configuration leaked")
	}
	if requests.Load() != 0 {
		t.Fatal("preflight performed I/O")
	}
	var rows int
	if err := a.DB.QueryRow(context.Background(), `SELECT count(*) FROM console_sessions WHERE state<>'released'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("preflight created a lease")
	}
}

func TestConsoleReadinessTracksMaintenanceBindingAndRole(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	s, d := seedConsole(t, a, admin, u, "success")
	path := "/api/v1/servers/" + s.ID + "/console-readiness"
	read := func(code string) {
		t.Helper()
		v := decodeTest[ConsoleReadiness](t, testRequest(t, a, "GET", path, nil, u), 200)
		if v.RequestAllowed || v.Code != code {
			t.Fatalf("wanted %s: %+v", code, v)
		}
	}
	if _, err := a.DB.Exec(context.Background(), `UPDATE servers SET maintenance=true WHERE id=$1`, s.ID); err != nil {
		t.Fatal(err)
	}
	read("MAINTENANCE")
	if _, err := a.DB.Exec(context.Background(), `UPDATE servers SET maintenance=false WHERE id=$1;`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(context.Background(), `UPDATE devices SET enabled=false WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	read("DEVICE_DISABLED")
	if _, err := a.DB.Exec(context.Background(), `UPDATE bindings SET active=false,ended_at=now() WHERE server_id=$1 AND kind='console'`, s.ID); err != nil {
		t.Fatal(err)
	}
	read("CONSOLE_BINDING_REQUIRED")
	if _, err := a.DB.Exec(context.Background(), `UPDATE users SET role='operator' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	u.Role = "operator"
	read("FORBIDDEN")
}

func TestConsoleReadinessExplainsInactiveOwnedService(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	other, _, _ := testUser(t, a, "customer")
	s, _ := seedConsole(t, a, admin, u, "success")
	ctx := context.Background()
	integrationID, mappingID := newID(), newID()
	if _, err := a.DB.Exec(ctx, `INSERT INTO integrations(id,name,secret_ref) VALUES($1,$2,'fixture')`, integrationID, "integration-"+integrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO service_mappings(id,integration_id,service_id,client_id,owner_id,server_id,state) VALUES($1,$2,'service','client',$3,$4,'suspended')`, mappingID, integrationID, u.ID, s.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + s.ID + "/console-readiness"
	for _, state := range []string{"suspended", "terminated"} {
		if _, err := a.DB.Exec(ctx, `UPDATE service_mappings SET state=$2 WHERE id=$1`, mappingID, state); err != nil {
			t.Fatal(err)
		}
		v := decodeTest[ConsoleReadiness](t, testRequest(t, a, "GET", path, nil, u), 200)
		if v.Code != "SERVICE_INACTIVE" || v.RequestAllowed {
			t.Fatalf("inactive owner preflight: %+v", v)
		}
		if w := testRequest(t, a, "GET", path, nil, other); w.Code != 404 {
			t.Fatalf("inactive server leaked to another customer: %d", w.Code)
		}
		if w := testRequest(t, a, "POST", "/api/v1/servers/"+s.ID+"/console-sessions", map[string]any{}, u); w.Code < 400 {
			t.Fatalf("preflight loosened action access: %d", w.Code)
		}
	}
	if _, err := a.DB.Exec(ctx, `UPDATE service_mappings SET state='active' WHERE id=$1`, mappingID); err != nil {
		t.Fatal(err)
	}
	v := decodeTest[ConsoleReadiness](t, testRequest(t, a, "GET", path, nil, u), 200)
	if !v.RequestAllowed || v.Code != "SIMULATION_READY" {
		t.Fatalf("reactivation preflight: %+v", v)
	}
}
