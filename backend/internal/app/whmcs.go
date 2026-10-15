package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var secretRefPattern = regexp.MustCompile(`^BAREPULSE_[A-Z0-9_]{3,100}$`)

func (a *App) registerWHMCS() {
	a.route("GET /api/v1/integrations", true, func(w http.ResponseWriter, r *http.Request, u User) error {
		rows, e := a.DB.Query(r.Context(), `SELECT id,name,secret_ref,enabled FROM integrations ORDER BY name`)
		if e != nil {
			return e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, name, ref string
			var on bool
			if e = rows.Scan(&id, &name, &ref, &on); e != nil {
				return e
			}
			out = append(out, map[string]any{"id": id, "name": name, "secret_ref": ref, "enabled": on})
		}
		if e = rows.Err(); e != nil {
			return e
		}
		jsonResponse(w, 200, out)
		return nil
	})
	a.route("POST /api/v1/integrations", true, func(w http.ResponseWriter, r *http.Request, u User) error {
		var b struct {
			Name      string `json:"name"`
			SecretRef string `json:"secret_ref"`
		}
		if e := decode(r, &b); e != nil {
			return e
		}
		if len(b.Name) < 1 || len(b.Name) > 100 || !secretRefPattern.MatchString(b.SecretRef) {
			return fail(400, "INVALID_INTEGRATION", "Name and BAREPULSE_ environment secret reference required")
		}
		tx, e := a.DB.Begin(r.Context())
		if e != nil {
			return e
		}
		defer tx.Rollback(r.Context())
		id := newID()
		if _, e = tx.Exec(r.Context(), `INSERT INTO integrations(id,name,secret_ref) VALUES($1,$2,$3)`, id, b.Name, b.SecretRef); e != nil {
			return dbError(e)
		}
		if e = audit(r.Context(), tx, u, "integration.create", id, map[string]string{"name": b.Name}); e != nil {
			return e
		}
		if e = tx.Commit(r.Context()); e != nil {
			return e
		}
		jsonResponse(w, 201, map[string]string{"id": id})
		return nil
	})
	a.route("GET /api/v1/service-mappings", true, func(w http.ResponseWriter, r *http.Request, u User) error {
		rows, e := a.DB.Query(r.Context(), `SELECT id,integration_id,service_id,client_id,owner_id,server_id,state,version FROM service_mappings ORDER BY created_at DESC`)
		if e != nil {
			return e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, instance, service, client, owner, server, state string
			var version int64
			if e = rows.Scan(&id, &instance, &service, &client, &owner, &server, &state, &version); e != nil {
				return e
			}
			out = append(out, map[string]any{"id": id, "integration_id": instance, "service_id": service, "client_id": client, "owner_id": owner, "server_id": server, "state": state, "version": version})
		}
		if e = rows.Err(); e != nil {
			return e
		}
		jsonResponse(w, 200, out)
		return nil
	})
	a.route("PUT /api/v1/integrations/{id}", true, a.updateIntegration)
	a.route("POST /api/v1/service-mappings", true, a.createMapping)
	a.route("POST /api/v1/service-mappings/{id}/state", true, a.adminMappingState)
	for _, verb := range []string{"GET", "POST"} {
		a.Mux.HandleFunc(verb+" /api/v1/whmcs/services/{service}/{action}", func(w http.ResponseWriter, r *http.Request) {
			if e := a.whmcsRequest(w, r); e != nil {
				a.writeError(w, e)
			}
		})
	}
	a.registerPortal()
}

// Disable or rotate an integration without deleting its history or touching devices.
// All issued portal credentials are destroyed, so re-enabling cannot revive them.
func (a *App) updateIntegration(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Name      string `json:"name"`
		SecretRef string `json:"secret_ref"`
		Enabled   *bool  `json:"enabled"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	if len(b.Name) < 1 || len(b.Name) > 100 || !secretRefPattern.MatchString(b.SecretRef) || b.Enabled == nil {
		return fail(400, "INVALID_INTEGRATION", "Name, BAREPULSE_ secret reference and explicit enabled state required")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	result, e := tx.Exec(r.Context(), `UPDATE integrations SET name=$2,secret_ref=$3,enabled=$4 WHERE id=$1`, r.PathValue("id"), b.Name, b.SecretRef, *b.Enabled)
	if e != nil {
		return dbError(e)
	}
	if result.RowsAffected() == 0 {
		return fail(404, "NOT_FOUND", "Integration not found")
	}
	if _, e = tx.Exec(r.Context(), `DELETE FROM portal_grants p USING service_mappings m WHERE p.mapping_id=m.id AND m.integration_id=$1`, r.PathValue("id")); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "integration.update", r.PathValue("id"), map[string]any{"name": b.Name, "enabled": *b.Enabled, "portal_credentials_revoked": true}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 200, map[string]any{"id": r.PathValue("id"), "enabled": *b.Enabled})
	return nil
}
func (a *App) createMapping(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		IntegrationID string `json:"integration_id"`
		ServiceID     string `json:"service_id"`
		ClientID      string `json:"client_id"`
		OwnerID       string `json:"owner_id"`
		ServerID      string `json:"server_id"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	if !numericID(b.ServiceID) || !numericID(b.ClientID) {
		return fail(400, "INVALID_MAPPING", "Positive numeric WHMCS service/client IDs required")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var owner string
	if e = tx.QueryRow(r.Context(), `SELECT owner_id FROM servers WHERE id=$1 FOR UPDATE`, b.ServerID).Scan(&owner); e != nil {
		return dbError(e)
	}
	if owner != b.OwnerID {
		return fail(409, "OWNER_MISMATCH", "Mapping owner must equal server owner")
	}
	var ownerRole string
	if e = tx.QueryRow(r.Context(), `SELECT role FROM users WHERE id=$1`, owner).Scan(&ownerRole); e != nil {
		return e
	}
	if ownerRole != "customer" {
		return fail(409, "OWNER_ROLE", "WHMCS mappings require a customer identity")
	}
	id := newID()
	if _, e = tx.Exec(r.Context(), `INSERT INTO service_mappings(id,integration_id,service_id,client_id,owner_id,server_id) VALUES($1,$2,$3,$4,$5,$6)`, id, b.IntegrationID, b.ServiceID, b.ClientID, b.OwnerID, b.ServerID); e != nil {
		return dbError(e)
	}
	if e = audit(r.Context(), tx, u, "service.bind", id, b); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 201, map[string]string{"id": id})
	return nil
}
func numericID(s string) bool {
	if len(s) < 1 || len(s) > 18 || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func (a *App) integrationAuth(r *http.Request, body []byte) (string, error) {
	id, stamp, nonce := r.Header.Get("X-BarePulse-Instance"), r.Header.Get("X-BarePulse-Timestamp"), r.Header.Get("X-BarePulse-Nonce")
	ts, e := strconv.ParseInt(stamp, 10, 64)
	if e != nil || time.Now().Unix()-ts > 60 || ts-time.Now().Unix() > 60 || len(nonce) < 24 || len(nonce) > 128 {
		return "", fail(401, "INTEGRATION_AUTH", "Invalid signature timestamp or nonce")
	}
	var ref string
	if e = a.DB.QueryRow(r.Context(), `SELECT secret_ref FROM integrations WHERE id=$1 AND enabled`, id).Scan(&ref); e != nil {
		return "", fail(401, "INTEGRATION_AUTH", "Integration not available")
	}
	secret := os.Getenv(ref)
	if len(secret) < 32 {
		return "", fail(503, "INTEGRATION_UNCONFIGURED", "Integration secret is not configured")
	}
	digest := sha256.Sum256(body)
	canonical := r.Method + "\n" + r.URL.RequestURI() + "\n" + stamp + "\n" + nonce + "\n" + hex.EncodeToString(digest[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	sig, e := hex.DecodeString(r.Header.Get("X-BarePulse-Signature"))
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", fail(401, "INTEGRATION_AUTH", "Invalid request signature")
	}
	tag, e := a.DB.Exec(r.Context(), `INSERT INTO integration_nonces(integration_id,nonce,expires_at) VALUES($1,$2,now()+interval '2 minutes') ON CONFLICT DO NOTHING`, id, nonce)
	if e != nil {
		return "", e
	}
	if tag.RowsAffected() != 1 {
		return "", fail(409, "REPLAY_REJECTED", "Authenticated nonce has already been consumed")
	}
	if _, e = a.DB.Exec(r.Context(), `DELETE FROM integration_nonces WHERE expires_at<now()`); e != nil {
		return "", e
	}
	return id, nil
}
func (a *App) whmcsRequest(w http.ResponseWriter, r *http.Request) error {
	body, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if e != nil || len(body) > 1<<20 {
		return fail(413, "BODY_TOO_LARGE", "Request too large")
	}
	instance, e := a.integrationAuth(r, body)
	if e != nil {
		return e
	}
	var b struct {
		ClientID       string `json:"client_id"`
		Action         string `json:"action,omitempty"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
		TaskID         string `json:"task_id,omitempty"`
		State          string `json:"state,omitempty"`
	}
	if r.Method == "GET" {
		b.ClientID = r.URL.Query().Get("client_id")
		b.TaskID = r.URL.Query().Get("task_id")
	} else {
		r.Body = io.NopCloser(bytes.NewReader(body))
		if e = decode(r, &b); e != nil {
			return e
		}
	}
	if !numericID(b.ClientID) {
		return fail(400, "INVALID_MAPPING", "Client ID required")
	}
	var mapping, server, owner, state string
	e = a.DB.QueryRow(r.Context(), `SELECT m.id,m.server_id,m.owner_id,m.state FROM service_mappings m JOIN servers s ON s.id=m.server_id WHERE m.integration_id=$1 AND m.service_id=$2 AND m.client_id=$3 AND m.owner_id=s.owner_id`, instance, r.PathValue("service"), b.ClientID).Scan(&mapping, &server, &owner, &state)
	if e != nil {
		return fail(404, "SERVICE_NOT_FOUND", "Service mapping not found for this client")
	}
	u := User{ID: owner, Role: "customer"}
	action := r.PathValue("action")
	if action == "lifecycle" && r.Method == "POST" {
		return a.changeMappingState(w, r, u, mapping, b.State)
	}
	if state != "active" {
		return fail(403, "SERVICE_INACTIVE", "Service access is suspended or terminated")
	}
	switch action {
	case "summary":
		if r.Method != "GET" {
			break
		}
		s, e := a.server(r.Context(), server, u)
		if e != nil {
			return e
		}
		jsonResponse(w, 200, map[string]any{"server": s, "service_id": r.PathValue("service"), "state": state})
		return nil
	case "power":
		if r.Method != "GET" {
			break
		}
		r.SetPathValue("id", server)
		return a.forwardCustomer(w, r, u, "GET", "/api/v1/servers/"+server+"/power", nil)
	case "traffic":
		if r.Method != "GET" {
			break
		}
		return a.forwardCustomer(w, r, u, "GET", "/api/v1/servers/"+server+"/traffic", nil)
	case "power-actions":
		if r.Method != "POST" {
			break
		}
		r.Header.Set("Idempotency-Key", b.IdempotencyKey)
		return a.forwardCustomer(w, r, u, "POST", "/api/v1/servers/"+server+"/power-actions", map[string]string{"action": b.Action})
	case "task":
		if r.Method != "GET" {
			break
		}
		var valid bool
		if e = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM operation_tasks WHERE id=$1 AND server_id=$2)`, b.TaskID, server).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return fail(404, "NOT_FOUND", "Task does not belong to this service")
		}
		return a.forwardCustomer(w, r, u, "GET", "/api/v1/tasks/"+b.TaskID, nil)
	case "console":
		if r.Method != "POST" {
			break
		}
		r.SetPathValue("id", server)
		r.Body = io.NopCloser(strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		capture := &responseCapture{header: make(http.Header)}
		if e = a.requestConsole(capture, r, u); e != nil {
			return e
		}
		var session ConsoleSession
		if e = json.Unmarshal(capture.body.Bytes(), &session); e != nil {
			return e
		}
		ticket := newID()
		if _, e = a.DB.Exec(r.Context(), `INSERT INTO portal_grants(token_hash,mapping_id,session_id,actor_id,kind,expires_at) VALUES($1,$2,$3,$4,'ticket',now()+interval '60 seconds')`, tokenHash(ticket), mapping, session.ID, u.ID); e != nil {
			return e
		}
		jsonResponse(w, 202, map[string]any{"session": session, "portal_url": a.Config.BaseURL + "/portal.html#" + ticket, "expires_in": 60})
		return nil
	}
	return fail(405, "ACTION_UNSUPPORTED", "Unsupported service action or method")
}

type responseCapture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *responseCapture) Header() http.Header         { return c.header }
func (c *responseCapture) WriteHeader(v int)           { c.status = v }
func (c *responseCapture) Write(b []byte) (int, error) { return c.body.Write(b) }

// Internal dispatch creates a short-lived server-side session; it never exports the session to WHMCS or its browser.
func (a *App) forwardCustomer(w http.ResponseWriter, r *http.Request, u User, method, path string, body any) error {
	token, csrf := newID(), newID()
	if _, e := a.DB.Exec(r.Context(), `INSERT INTO auth_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '1 minute')`, tokenHash(token), u.ID, csrf); e != nil {
		return e
	}
	defer a.DB.Exec(r.Context(), `DELETE FROM auth_sessions WHERE token_hash=$1`, tokenHash(token))
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, e := http.NewRequestWithContext(r.Context(), method, path, &buf)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Idempotency-Key", r.Header.Get("Idempotency-Key"))
	req.AddCookie(&http.Cookie{Name: "bp_session", Value: token})
	a.Mux.ServeHTTP(w, req)
	return nil
}
func (a *App) adminMappingState(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		State string `json:"state"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	return a.changeMappingState(w, r, u, r.PathValue("id"), b.State)
}
func (a *App) changeMappingState(w http.ResponseWriter, r *http.Request, u User, id, state string) error {
	if state != "active" && state != "suspended" && state != "terminated" {
		return fail(400, "INVALID_STATE", "Choose active, suspended or terminated")
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var server, previous string
	if e = tx.QueryRow(r.Context(), `SELECT server_id FROM service_mappings WHERE id=$1`, id).Scan(&server); e != nil {
		return dbError(e)
	}
	var lockedVersion int64
	var currentOwner string
	if e = tx.QueryRow(r.Context(), `SELECT version,owner_id FROM servers WHERE id=$1 FOR UPDATE`, server).Scan(&lockedVersion, &currentOwner); e != nil {
		return e
	}
	if e = tx.QueryRow(r.Context(), `SELECT server_id,state FROM service_mappings WHERE id=$1 FOR UPDATE`, id).Scan(&server, &previous); e != nil {
		return dbError(e)
	}
	if previous == "terminated" && state != "terminated" {
		return fail(409, "SERVICE_TERMINATED", "A terminated mapping cannot be revived")
	}
	if previous == "terminated" && state == "terminated" {
		if e = tx.Commit(r.Context()); e != nil {
			return e
		}
		jsonResponse(w, 200, map[string]string{"state": "terminated", "physical_policy": "disabled"})
		return nil
	}
	var mappingOwner string
	if e = tx.QueryRow(r.Context(), `SELECT owner_id FROM service_mappings WHERE id=$1`, id).Scan(&mappingOwner); e != nil {
		return e
	}
	if mappingOwner != currentOwner || (u.Role != "admin" && u.ID != currentOwner) {
		return fail(409, "OWNER_MISMATCH", "Service ownership changed")
	}
	if state == "terminated" {
		var busy bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM console_sessions WHERE server_id=$1 AND state NOT IN ('released','queued')) OR EXISTS(SELECT 1 FROM operation_tasks WHERE server_id=$1 AND status IN ('running','verifying','unknown'))`, server).Scan(&busy); e != nil {
			return e
		}
		if busy {
			return fail(409, "SERVICE_BUSY", "Suspend access first; reconcile active tasks and protected sessions before termination")
		}
	}
	if _, e = tx.Exec(r.Context(), `UPDATE service_mappings SET state=$2,version=version+1,updated_at=now() WHERE id=$1`, id, state); e != nil {
		return e
	}
	if state != "active" {
		if _, e = tx.Exec(r.Context(), `UPDATE operation_tasks SET status='cancelled',result='{"code":"SERVICE_INACTIVE"}',updated_at=now() WHERE server_id=$1 AND status='queued'`, server); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `UPDATE console_sessions SET generation=generation+1,state=CASE WHEN state='protected' THEN 'protected' WHEN state='queued' THEN 'released' ELSE 'revoking' END,updated_at=now() WHERE server_id=$1 AND state<>'released'`, server); e != nil {
			return e
		}
		if _, e = tx.Exec(r.Context(), `DELETE FROM portal_grants WHERE mapping_id=$1`, id); e != nil {
			return e
		}
	}
	if e = audit(r.Context(), tx, u, "service.lifecycle", id, map[string]string{"state": state, "previous": previous}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 200, map[string]string{"state": state, "physical_policy": "disabled"})
	return nil
}
