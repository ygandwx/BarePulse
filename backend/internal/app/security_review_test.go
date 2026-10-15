package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Delay the request body after initial authorization so lifecycle changes happen
// deterministically between permission checks and the eventual mutation.
type securityPausedBody struct {
	started chan struct{}
	resume  chan struct{}
	once    sync.Once
	data    io.Reader
}

func (b *securityPausedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.resume })
	return b.data.Read(p)
}
func (*securityPausedBody) Close() error { return nil }
func securityPauseBody(value string) *securityPausedBody {
	return &securityPausedBody{started: make(chan struct{}), resume: make(chan struct{}), data: strings.NewReader(value)}
}
func securityRequest(t *testing.T, a *App, u User, path, contentType string, body io.Reader) *http.Request {
	t.Helper()
	token, csrf := newID(), newID()
	if _, err := a.DB.Exec(context.Background(), `INSERT INTO auth_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, tokenHash(token), u.ID, csrf); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(&http.Cookie{Name: "bp_session", Value: token})
	return r
}
func securityAwaitBody(t *testing.T, b *securityPausedBody, done <-chan struct{}) {
	t.Helper()
	select {
	case <-b.started:
	case <-done:
		t.Fatal("request ended before body was consumed")
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach body read")
	}
}
func securityAwaitResponse(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish after body release")
	}
}

func TestSecurityInputRevalidatesAfterBodyRead(t *testing.T) {
	for _, transition := range []string{"role_revoked", "service_suspended", "logged_out"} {
		t.Run(transition, func(t *testing.T) {
			a := testApp(t)
			admin, u, s, _, mapping, _ := setupWHMCS(t, a)
			session := requestConsoleTest(t, a, u, s.ID)
			if transition == "service_suspended" {
				decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+session.ID+"/protection", map[string]any{"enabled": true, "reason": "installation must survive suspension"}, u), 200)
			}
			_, token := consoleGrant(t, a, u, session.ID)
			body := securityPauseBody(`{"type":"key","key":"Enter","pressed":true}`)
			r := securityRequest(t, a, u, "/api/v1/gateway/"+session.ID+"/input", "application/json", body)
			r.Header.Set("X-Console-Token", token)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); a.Handler().ServeHTTP(w, r) }()
			securityAwaitBody(t, body, done)
			if transition == "role_revoked" {
				if _, err := a.DB.Exec(context.Background(), `UPDATE users SET role='operator' WHERE id=$1`, u.ID); err != nil {
					close(body.resume)
					t.Fatal(err)
				}
			} else if transition == "logged_out" {
				cookie, err := r.Cookie("bp_session")
				if err != nil {
					close(body.resume)
					t.Fatal(err)
				}
				if _, err = a.DB.Exec(context.Background(), `DELETE FROM auth_sessions WHERE token_hash=$1`, tokenHash(cookie.Value)); err != nil {
					close(body.resume)
					t.Fatal(err)
				}
			} else {
				response := testRequest(t, a, "POST", "/api/v1/service-mappings/"+mapping+"/state", map[string]string{"state": "suspended"}, admin)
				if response.Code != 200 {
					close(body.resume)
					t.Fatalf("suspend %d %s", response.Code, response.Body)
				}
			}
			close(body.resume)
			securityAwaitResponse(t, done)
			var keys string
			if err := a.DB.QueryRow(context.Background(), `SELECT pressed_keys::text FROM mock_console_state WHERE session_id=$1`, session.ID).Scan(&keys); err != nil {
				t.Fatal(err)
			}
			if w.Code < 400 || keys != "[]" {
				t.Fatalf("input authorized before %s mutated after revocation: status=%d keys=%s body=%s", transition, w.Code, keys, w.Body)
			}
		})
	}
}

func TestSecurityProtectedUploadRevalidatesServiceAfterBody(t *testing.T) {
	a := testApp(t)
	admin, u, s, _, mapping, _ := setupWHMCS(t, a)
	session := requestConsoleTest(t, a, u, s.ID)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+session.ID+"/protection", map[string]any{"enabled": true, "reason": "installation must survive suspension"}, u), 200)
	body := securityPauseBody("offline-iso-test-fixture")
	r := securityRequest(t, a, u, "/api/v1/console-sessions/"+session.ID+"/media", "application/octet-stream", body)
	r.Header.Set("X-Filename", "review.iso")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); a.Handler().ServeHTTP(w, r) }()
	securityAwaitBody(t, body, done)
	response := testRequest(t, a, "POST", "/api/v1/service-mappings/"+mapping+"/state", map[string]string{"state": "suspended"}, admin)
	if response.Code != 200 {
		close(body.resume)
		t.Fatalf("suspend %d %s", response.Code, response.Body)
	}
	close(body.resume)
	securityAwaitResponse(t, done)
	var count int
	if err := a.DB.QueryRow(context.Background(), `SELECT count(*) FROM media_assets WHERE session_id=$1`, session.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if w.Code < 400 || count != 0 {
		t.Fatalf("protected suspended upload persisted: status=%d assets=%d body=%s", w.Code, count, w.Body)
	}
}

func TestSecurityPortalDoesNotRestoreRevokedRole(t *testing.T) {
	a := testApp(t)
	_, u, _, instance, _, secret := setupWHMCS(t, a)
	entry := decodeTest[struct {
		Session ConsoleSession `json:"session"`
		URL     string         `json:"portal_url"`
	}](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/console", newID(), map[string]string{"client_id": "201"}), 202)
	ticket := strings.TrimPrefix(entry.URL, a.Config.BaseURL+"/portal.html#")
	grant := decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/portal/redeem", map[string]string{"ticket": ticket}, User{}), 201)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+entry.Session.ID+"/protection", map[string]any{"enabled": true, "reason": "persisted installation protection"}, u), 200)
	if _, err := a.DB.Exec(context.Background(), `UPDATE users SET role='operator' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]bool{"enabled": false})
	r := httptest.NewRequest("POST", "/api/v1/portal/protection", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Portal-Token", grant["portal_token"].(string))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	var state string
	if err := a.DB.QueryRow(context.Background(), `SELECT state FROM console_sessions WHERE id=$1`, entry.Session.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if w.Code != 403 || state != "protected" {
		t.Fatalf("portal token restored downgraded customer's control: status=%d state=%s body=%s", w.Code, state, w.Body)
	}
}

func TestSecuritySuspendedCustomerCannotClearProtection(t *testing.T) {
	a := testApp(t)
	admin, u, s, _, mapping, _ := setupWHMCS(t, a)
	session := requestConsoleTest(t, a, u, s.ID)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+session.ID+"/protection", map[string]any{"enabled": true, "reason": "persisted installation protection"}, u), 200)
	decodeTest[map[string]string](t, testRequest(t, a, "POST", "/api/v1/service-mappings/"+mapping+"/state", map[string]string{"state": "suspended"}, admin), 200)
	response := testRequest(t, a, "POST", "/api/v1/console-sessions/"+session.ID+"/protection", map[string]bool{"enabled": false}, u)
	var state string
	if err := a.DB.QueryRow(context.Background(), `SELECT state FROM console_sessions WHERE id=$1`, session.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if response.Code < 400 || state != "protected" {
		t.Fatalf("suspended customer cleared protection: status=%d state=%s body=%s", response.Code, state, response.Body)
	}
}

func TestSecurityConsoleMutationUsesItsTransactionConnection(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	server, _ := seedConsole(t, a, admin, u, "success")
	session := requestConsoleTest(t, a, u, server.ID)
	config := a.DB.Config()
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	limited := New(pool, a.Config)
	request := securityRequest(t, a, u, "/api/v1/console-sessions/"+session.ID+"/media", "application/octet-stream", strings.NewReader("fixture"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	tx, _, err := limited.lockConsoleMutation(request, u, session, false)
	if err != nil {
		t.Fatalf("mutation requires an additional pooled connection while owning its transaction: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityNewServiceIgnoresTerminatedMappingHistory(t *testing.T) {
	for _, scenario := range []string{"new_console", "old_terminate_replay"} {
		t.Run(scenario, func(t *testing.T) {
			a := testApp(t)
			admin, u, server, instance, oldMapping, secret := setupWHMCS(t, a)
			decodeTest[map[string]string](t, testRequest(t, a, "POST", "/api/v1/service-mappings/"+oldMapping+"/state", map[string]string{"state": "terminated"}, admin), 200)
			decodeTest[map[string]string](t, testRequest(t, a, "POST", "/api/v1/service-mappings", map[string]string{"integration_id": instance, "service_id": "102", "client_id": "202", "owner_id": u.ID, "server_id": server.ID}, admin), 201)
			if scenario == "new_console" {
				response := signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/102/console", newID(), map[string]string{"client_id": "202"})
				if response.Code != 202 {
					t.Fatalf("historical terminated mapping blocked new active console: %d %s", response.Code, response.Body)
				}
			} else {
				task := decodeTest[OperationTask](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/102/power-actions", newID(), map[string]string{"client_id": "202", "action": "power_on", "idempotency_key": "new-service-pending-power"}), 202)
				response := signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/lifecycle", newID(), map[string]string{"client_id": "201", "state": "terminated"})
				if response.Code != 200 {
					t.Fatalf("old terminal state was not idempotent: %d %s", response.Code, response.Body)
				}
				var state string
				if err := a.DB.QueryRow(context.Background(), `SELECT status FROM operation_tasks WHERE id=$1`, task.ID).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if state != "queued" {
					t.Fatalf("repeating old service termination mutated new service task: %s", state)
				}
			}
		})
	}
}

func TestSecuritySessionListHidesReassignedAssets(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	previous, _, _ := testUser(t, a, "customer")
	next, _, _ := testUser(t, a, "customer")
	server, _ := seedConsole(t, a, admin, previous, "success")
	session := requestConsoleTest(t, a, previous, server.ID)
	decodeTest[ConsoleSession](t, testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+session.ID, nil, previous), 202)
	current := decodeTest[Server](t, testRequest(t, a, "GET", "/api/v1/servers/"+server.ID, nil, admin), 200)
	decodeTest[Server](t, testRequest(t, a, "PUT", "/api/v1/servers/"+server.ID, map[string]any{"name": current.Name, "owner_id": next.ID, "version": current.Version}, admin), 200)
	sessions := decodeTest[[]ConsoleSession](t, testRequest(t, a, "GET", "/api/v1/console-sessions", nil, previous), 200)
	for _, s := range sessions {
		if s.ServerID == server.ID {
			t.Fatalf("previous customer still receives reassigned asset in session list: %+v", s)
		}
	}
}

func TestSecurityProtectionRechecksRoleAfterSlowBody(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	server, _ := seedConsole(t, a, admin, u, "success")
	session := requestConsoleTest(t, a, u, server.ID)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+session.ID+"/protection", map[string]any{"enabled": true, "reason": "installation must remain protected"}, u), 200)
	body := securityPauseBody(`{"enabled":false}`)
	r := securityRequest(t, a, u, "/api/v1/console-sessions/"+session.ID+"/protection", "application/json", body)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); a.Handler().ServeHTTP(w, r) }()
	securityAwaitBody(t, body, done)
	if _, err := a.DB.Exec(context.Background(), `UPDATE users SET role='operator' WHERE id=$1`, u.ID); err != nil {
		close(body.resume)
		t.Fatal(err)
	}
	close(body.resume)
	securityAwaitResponse(t, done)
	var state string
	if err := a.DB.QueryRow(context.Background(), `SELECT state FROM console_sessions WHERE id=$1`, session.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if w.Code < 400 || state != "protected" {
		t.Fatalf("slow protection request retained revoked authority: status=%d state=%s body=%s", w.Code, state, w.Body)
	}
}

func TestSecurityOperatorCannotDeleteMedia(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	server, _ := seedConsole(t, a, admin, u, "success")
	session := requestConsoleTest(t, a, u, server.ID)
	r := securityRequest(t, a, u, "/api/v1/console-sessions/"+session.ID+"/media", "application/octet-stream", strings.NewReader("offline fixture"))
	r.Header.Set("X-Filename", "review.iso")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	asset := decodeTest[map[string]string](t, w, 201)
	if _, err := a.DB.Exec(context.Background(), `UPDATE users SET role='operator' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	response := testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+session.ID+"/media/"+asset["id"], nil, u)
	var state string
	if err := a.DB.QueryRow(context.Background(), `SELECT state FROM media_assets WHERE id=$1`, asset["id"]).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if response.Code != 403 || state != "ready" {
		t.Fatalf("read-only operator deleted media: status=%d state=%s body=%s", response.Code, state, response.Body)
	}
}
