package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func signedWHMCS(t *testing.T, a *App, instance, secret, method, path, nonce string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	digest := sha256.Sum256(buf.Bytes())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(method + "\n" + path + "\n" + stamp + "\n" + nonce + "\n" + hex.EncodeToString(digest[:])))
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-BarePulse-Instance", instance)
	r.Header.Set("X-BarePulse-Timestamp", stamp)
	r.Header.Set("X-BarePulse-Nonce", nonce)
	r.Header.Set("X-BarePulse-Signature", hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func setupWHMCS(t *testing.T, a *App) (User, User, Server, string, string, string) {
	t.Helper()
	admin, _, _ := testUser(t, a, "admin")
	u, _, _ := testUser(t, a, "customer")
	s, _ := seedConsole(t, a, admin, u, "success")
	secret := "offline-whmcs-test-secret-at-least-32-bytes"
	t.Setenv("BAREPULSE_TEST_WH_SECRET", secret)
	instance := decodeTest[map[string]string](t, testRequest(t, a, "POST", "/api/v1/integrations", map[string]string{"name": "test-instance", "secret_ref": "BAREPULSE_TEST_WH_SECRET"}, admin), 201)["id"]
	mapping := decodeTest[map[string]string](t, testRequest(t, a, "POST", "/api/v1/service-mappings", map[string]string{"integration_id": instance, "service_id": "101", "client_id": "201", "owner_id": u.ID, "server_id": s.ID}, admin), 201)["id"]
	return admin, u, s, instance, mapping, secret
}
func TestWHMCSOwnershipSignatureReplayAndLifecycle(t *testing.T) {
	a := testApp(t)
	admin, u, s, instance, _, secret := setupWHMCS(t, a)
	nonce := newID()
	path := "/api/v1/whmcs/services/101/summary?client_id=201"
	decodeTest[map[string]any](t, signedWHMCS(t, a, instance, secret, "GET", path, nonce, nil), 200)
	if w := signedWHMCS(t, a, instance, secret, "GET", path, nonce, nil); w.Code != 409 {
		t.Fatalf("nonce replay: %d", w.Code)
	}
	if w := signedWHMCS(t, a, instance, secret, "GET", "/api/v1/whmcs/services/101/summary?client_id=999", newID(), nil); w.Code != 404 {
		t.Fatal("forged client accessed service")
	}
	if w := signedWHMCS(t, a, instance, "wrong-secret", "GET", path, newID(), nil); w.Code != 401 {
		t.Fatal("invalid HMAC accepted")
	}
	body := map[string]string{"client_id": "201", "action": "power_on", "idempotency_key": "whmcs-stable-operation-1"}
	task := decodeTest[OperationTask](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/power-actions", newID(), body), 202)
	again := decodeTest[OperationTask](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/power-actions", newID(), body), 200)
	if task.ID != again.ID {
		t.Fatal("integration duplicated power task")
	}
	decodeTest[map[string]string](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/lifecycle", newID(), map[string]string{"client_id": "201", "state": "suspended"}), 200)
	if w := signedWHMCS(t, a, instance, secret, "GET", path, newID(), nil); w.Code != 403 {
		t.Fatal("suspended service accessible")
	}
	if w := powerRequest(t, a, u, s.ID, "power_on", "direct-after-suspend"); w.Code < 400 {
		t.Fatal("suspension bypassed through platform")
	}
	stopped := decodeTest[OperationTask](t, testRequest(t, a, "GET", "/api/v1/tasks/"+task.ID, nil, admin), 200)
	if stopped.Status != "cancelled" {
		t.Fatalf("queued task not cancelled: %s", stopped.Status)
	}
	var dispatched int
	if e := a.DB.QueryRow(context.Background(), `SELECT count(*) FROM operation_tasks WHERE status IN ('running','succeeded')`).Scan(&dispatched); e != nil {
		t.Fatal(e)
	}
	if dispatched != 0 {
		t.Fatal("suspend caused physical operation")
	}
}
func TestWHMCSPortalSingleUseAndProtectionOnSuspend(t *testing.T) {
	a := testApp(t)
	admin, u, _, instance, _, secret := setupWHMCS(t, a)
	console := decodeTest[struct {
		Session ConsoleSession `json:"session"`
		URL     string         `json:"portal_url"`
	}](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/console", newID(), map[string]string{"client_id": "201"}), 202)
	ticket := console.URL[len(a.Config.BaseURL+"/portal.html#"):]
	grant := decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/portal/redeem", map[string]string{"ticket": ticket}, User{}), 201)
	if w := testRequest(t, a, "POST", "/api/v1/portal/redeem", map[string]string{"ticket": ticket}, User{}); w.Code != 403 {
		t.Fatal("portal replay accepted")
	}
	r := httptest.NewRequest("GET", "/api/v1/portal/session", nil)
	r.Header.Set("X-Portal-Token", grant["portal_token"].(string))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	decodeTest[ConsoleSession](t, w, 200)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+console.Session.ID+"/protection", map[string]any{"enabled": true, "reason": "WHMCS installation in progress"}, u), 200)
	decodeTest[map[string]string](t, signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/lifecycle", newID(), map[string]string{"client_id": "201", "state": "suspended"}), 200)
	r = httptest.NewRequest("GET", "/api/v1/portal/session", nil)
	r.Header.Set("X-Portal-Token", grant["portal_token"].(string))
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("suspended portal token survived")
	}
	c := decodeTest[ConsoleSession](t, testRequest(t, a, "GET", "/api/v1/console-sessions/"+console.Session.ID, nil, admin), 200)
	if c.State != "protected" {
		t.Fatal("suspension removed installation protection")
	}
	if w = signedWHMCS(t, a, instance, secret, "POST", "/api/v1/whmcs/services/101/lifecycle", newID(), map[string]string{"client_id": "201", "state": "terminated"}); w.Code != 409 {
		t.Fatal("protected server terminated")
	}
}
