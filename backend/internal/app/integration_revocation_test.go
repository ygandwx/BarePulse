package app

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntegrationRevocationRotationPreservesProtection(t *testing.T) {
	a := testApp(t)
	admin, customer, _, instance, _, oldSecret := setupWHMCS(t, a)
	entry := decodeTest[struct {
		Session ConsoleSession `json:"session"`
		URL     string         `json:"portal_url"`
	}](t, signedWHMCS(t, a, instance, oldSecret, "POST", "/api/v1/whmcs/services/101/console", newID(), map[string]string{"client_id": "201"}), 202)
	ticket := strings.TrimPrefix(entry.URL, a.Config.BaseURL+"/portal.html#")
	grant := decodeTest[map[string]any](t, testRequest(t, a, "POST", "/api/v1/portal/redeem", map[string]string{"ticket": ticket}, User{}), 201)
	decodeTest[ConsoleSession](t, testRequest(t, a, "POST", "/api/v1/console-sessions/"+entry.Session.ID+"/protection", map[string]any{"enabled": true, "reason": "installation during integration rotation"}, customer), 200)
	body := map[string]any{"name": "renamed integration", "secret_ref": "BAREPULSE_TEST_NEW_SECRET", "enabled": false}
	path := "/api/v1/integrations/" + instance
	if w := testRequest(t, a, "PUT", path, body, customer); w.Code != 403 {
		t.Fatalf("customer modified integration: %d %s", w.Code, w.Body)
	}
	if w := testRequest(t, a, "PUT", path, map[string]string{"name": "invalid", "secret_ref": "BAREPULSE_TEST_NEW_SECRET"}, admin); w.Code != 400 {
		t.Fatal("missing enabled state accepted")
	}
	decodeTest[map[string]any](t, testRequest(t, a, "PUT", path, body, admin), 200)
	service := "/api/v1/whmcs/services/101/summary?client_id=201"
	if w := signedWHMCS(t, a, instance, oldSecret, "GET", service, newID(), nil); w.Code != 401 {
		t.Fatalf("disabled integration accepted: %d", w.Code)
	}
	assertRevoked := func() {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/portal/session", nil)
		r.Header.Set("X-Portal-Token", grant["portal_token"].(string))
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("old portal grant revived: %d", w.Code)
		}
		if w := testRequest(t, a, "POST", "/api/v1/portal/redeem", map[string]string{"ticket": ticket}, User{}); w.Code != 403 {
			t.Fatal("old portal ticket revived")
		}
	}
	assertRevoked()
	newSecret := "new-offline-whmcs-test-secret-at-least-32-bytes"
	t.Setenv("BAREPULSE_TEST_NEW_SECRET", newSecret)
	body["enabled"] = true
	decodeTest[map[string]any](t, testRequest(t, a, "PUT", path, body, admin), 200)
	if w := signedWHMCS(t, a, instance, oldSecret, "GET", service, newID(), nil); w.Code != 401 {
		t.Fatal("old HMAC secret accepted after rotation")
	}
	decodeTest[map[string]any](t, signedWHMCS(t, a, instance, newSecret, "GET", service, newID(), nil), 200)
	assertRevoked()
	current := decodeTest[ConsoleSession](t, testRequest(t, a, "GET", "/api/v1/console-sessions/"+entry.Session.ID, nil, admin), 200)
	if current.State != "protected" || current.ProtectionReason != "installation during integration rotation" {
		t.Fatal("integration change cleared installation protection")
	}
	var tasks int
	if e := a.DB.QueryRow(context.Background(), `SELECT count(*) FROM operation_tasks`).Scan(&tasks); e != nil {
		t.Fatal(e)
	}
	if tasks != 0 {
		t.Fatal("integration change created physical actions")
	}
}
