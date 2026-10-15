package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedConsole(t *testing.T, a *App, admin, owner User, scenario string) (Server, Device) {
	t.Helper()
	s, d, _ := seedFoundation(t, a, admin, owner, "mock_pikvm", scenario)
	decodeTest[Binding](t, testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s.ID, "device_id": d.ID, "kind": "console", "port": 1}, admin), 201)
	return s, d
}
func requestConsoleTest(t *testing.T, a *App, u User, server string) ConsoleSession {
	t.Helper()
	return decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/servers/"+server+"/console-sessions", map[string]any{}, u), 202)
}
func consoleGrant(t *testing.T, a *App, u User, id string) (string, string) {
	t.Helper()
	ticket := decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+id+"/tickets", map[string]any{}, u), 201)["ticket"].(string)
	grant := decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+id+"/redeem", map[string]string{"ticket": ticket}, u), 201)
	return ticket, grant["access_token"].(string)
}
func TestConsoleExclusiveProtectionRestartAndCleanup(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	alice, _, _ := testUser(t, a, "customer")
	bob, _, _ := testUser(t, a, "customer")
	s, d := seedConsole(t, a, admin, alice, "success")
	s2 := decodeTest[Server](t, testRequest(t, a, "POST", "/api/v1/servers", map[string]any{"name": "other-server", "owner_id": bob.ID}, admin), 201)
	decodeTest[Binding](t, testRequest(t, a, "POST", "/api/v1/bindings", map[string]any{"server_id": s2.ID, "device_id": d.ID, "kind": "console", "port": 2}, admin), 201)
	c := requestConsoleTest(t, a, alice, s.ID)
	if c.State != "active" {
		t.Fatalf("%+v", c)
	}
	q := requestConsoleTest(t, a, bob, s2.ID)
	if q.State != "queued" {
		t.Fatalf("second owner acquired %+v", q)
	}
	if w := testRequest(t, a, "GET", "/api/v1/console-sessions/"+c.ID, nil, bob); w.Code != 404 {
		t.Fatal("other customer saw session")
	}
	ticket, _ := consoleGrant(t, a, alice, c.ID)
	if w := testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/redeem", map[string]string{"ticket": ticket}, alice); w.Code != 403 {
		t.Fatal("ticket replay accepted")
	}
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/protection", map[string]any{"enabled": true, "reason": "Installing test operating system"}, alice), 200)
	_, e := a.DB.Exec(context.Background(), `UPDATE console_sessions SET expires_at=now()-interval '1 hour' WHERE id=$1`, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	restarted := New(a.DB, a.Config)
	if e = restarted.ConsoleTick(context.Background()); e != nil {
		t.Fatal(e)
	}
	restored := decodeTest[ConsoleSession](t, testRequest(t, restarted, "GET", "/api/v1/console-sessions/"+c.ID, nil, alice), 200)
	if restored.State != "protected" || restored.ProtectionReason == "" {
		t.Fatal("restart dropped protection")
	}
	if w := testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID, nil, alice); w.Code != 409 {
		t.Fatal("protected session released")
	}
	if w := powerRequest(t, a, alice, s.ID, "hard_reset", "protection-conflict"); w.Code != 409 {
		t.Fatalf("protected power accepted: %d %s", w.Code, w.Body)
	}
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/protection", map[string]any{"enabled": false}, alice), 200)
	_, e = a.DB.Exec(context.Background(), `UPDATE mock_console_state SET pressed_keys='["ControlLeft"]' WHERE device_id=$1`, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	released := decodeTest[ConsoleSession](t, testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID, nil, alice), 202)
	if released.State != "released" {
		t.Fatalf("not cleaned %+v", released)
	}
	if e = a.ConsoleTick(context.Background()); e != nil {
		t.Fatal(e)
	}
	q = decodeTest[ConsoleSession](t, testRequest(t, a, "GET", "/api/v1/console-sessions/"+q.ID, nil, bob), 200)
	if q.State != "active" {
		t.Fatal("queue not advanced")
	}
	var keys string
	var port int
	if e = a.DB.QueryRow(context.Background(), `SELECT pressed_keys::text,selected_port FROM mock_console_state WHERE device_id=$1`, d.ID).Scan(&keys, &port); e != nil {
		t.Fatal(e)
	}
	if keys != "[]" || port != 2 {
		t.Fatalf("residual input or target %s %d", keys, port)
	}
}
func TestConsoleUnknownTargetAndMockReconciliation(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	s, d := seedConsole(t, a, admin, u, "timeout")
	c := requestConsoleTest(t, a, u, s.ID)
	if c.State != "blocked" {
		t.Fatal("unverified target became active")
	}
	if w := testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/tickets", map[string]any{}, u); w.Code != 409 {
		t.Fatal("unknown target got ticket")
	}
	if w := testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/reconcile", map[string]any{"reason": "mock failure reconciled", "connections_stopped": true}, u); w.Code != 403 {
		t.Fatal("customer reconciled device")
	}
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/reconcile", map[string]any{"reason": "mock failure reconciled", "connections_stopped": true}, admin), 200)
	next := requestConsoleTest(t, a, u, s.ID)
	if next.State != "active" {
		t.Fatal("mock device cannot recover")
	}
	if _, e := a.DB.Exec(context.Background(), `UPDATE mock_console_state SET selected_port=NULL WHERE device_id=$1`, d.ID); e != nil {
		t.Fatal(e)
	}
	if w := testRequest(t, a, "POST", "/api/v1/console-sessions/"+next.ID+"/tickets", map[string]any{}, u); w.Code != 409 {
		t.Fatal("missing target accepted as port one")
	}
	if w := testRequest(t, a, "GET", "/api/v1/gateway/"+next.ID+"/extras/terminal", nil, u); w.Code != 403 {
		t.Fatal("unapproved proxy channel exposed")
	}
}
func TestConsoleLiveStreamRevokedBeforeHandoff(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, cookie := testUser(t, a, "customer")
	s, _ := seedConsole(t, a, admin, u, "success")
	c := requestConsoleTest(t, a, u, s.ID)
	_, access := consoleGrant(t, a, u, c.ID)
	host := httptest.NewServer(a.Handler())
	defer host.Close()
	req, _ := http.NewRequest("GET", host.URL+"/api/v1/gateway/"+c.ID+"/events", nil)
	req.AddCookie(&http.Cookie{Name: "bp_session", Value: cookie})
	req.Header.Set("X-Console-Token", access)
	client := &http.Client{Timeout: 5 * time.Second}
	res, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	line, e := reader.ReadString('\n')
	if e != nil || !strings.Contains(line, "frame") {
		t.Fatalf("no live stream %q %v", line, e)
	}
	decodeTest[ConsoleSession](t, testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID, nil, u), 202)
	if _, e = io.ReadAll(reader); e != nil {
		t.Fatalf("stream did not close cleanly: %v", e)
	}
	if e = a.ConsoleTick(context.Background()); e != nil {
		t.Fatal(e)
	}
	released := decodeTest[ConsoleSession](t, testRequest(t, a, "GET", "/api/v1/console-sessions/"+c.ID, nil, u), 200)
	if released.State != "released" {
		t.Fatalf("stream cleanup incomplete %+v", released)
	}
}
func TestConsoleMediaIsolationProtectionAndRoleRevocation(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, csrf, cookie := testUser(t, a, "customer")
	s, _ := seedConsole(t, a, admin, u, "success")
	c := requestConsoleTest(t, a, u, s.ID)
	req := httptest.NewRequest("POST", "/api/v1/console-sessions/"+c.ID+"/media", bytes.NewBufferString("offline-iso-fixture-not-a-real-boot-image"))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Filename", "fixture.iso")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: "bp_session", Value: cookie})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	asset := decodeTest[map[string]string](t, w, 201)
	decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/media/"+asset["id"]+"/mount", map[string]any{}, u), 200)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/protection", map[string]any{"enabled": true, "reason": "ISO installation"}, u), 200)
	if w = testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID+"/media/"+asset["id"]+"/mount", nil, u); w.Code != 409 {
		t.Fatal("protected ISO detached")
	}
	if _, e := a.DB.Exec(context.Background(), `UPDATE users SET role='operator' WHERE id=$1`, u.ID); e != nil {
		t.Fatal(e)
	}
	if w = testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/tickets", map[string]any{}, u); w.Code != 403 {
		t.Fatal("downgraded operator got access")
	}
	var media string
	if e := a.DB.QueryRow(context.Background(), `SELECT state FROM media_assets WHERE id=$1`, asset["id"]).Scan(&media); e != nil {
		t.Fatal(e)
	}
	if media != "mounted" {
		t.Fatal("role change destroyed protected media")
	}
	// Admin can explicitly finish the job and then clean up; suspension/revocation alone must not unmount it.
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+c.ID+"/protection", map[string]any{"enabled": false}, admin), 200)
	decodeTest[ConsoleSession](t, testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID, nil, admin), 202)
	var raw json.RawMessage
	_ = raw
}
