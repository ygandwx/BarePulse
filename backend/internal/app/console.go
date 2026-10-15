package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

type ConsoleSession struct {
	ID               string    `json:"id"`
	ActorID          string    `json:"actor_id"`
	ServerID         string    `json:"server_id"`
	DeviceID         string    `json:"device_id"`
	BindingID        string    `json:"binding_id"`
	BindingVersion   int64     `json:"binding_version"`
	ServerVersion    int64     `json:"server_version"`
	DeviceVersion    int64     `json:"device_version"`
	Port             int       `json:"port"`
	Generation       int64     `json:"generation"`
	State            string    `json:"state"`
	ProtectionReason string    `json:"protection_reason"`
	ErrorCode        string    `json:"error_code"`
	ExpiresAt        time.Time `json:"expires_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

const consoleColumns = `id,actor_id,server_id,device_id,binding_id,binding_version,server_version,device_version,port,generation,state,protection_reason,error_code,expires_at,created_at,updated_at`

func scanConsole(row pgx.Row) (ConsoleSession, error) {
	var s ConsoleSession
	e := row.Scan(&s.ID, &s.ActorID, &s.ServerID, &s.DeviceID, &s.BindingID, &s.BindingVersion, &s.ServerVersion, &s.DeviceVersion, &s.Port, &s.Generation, &s.State, &s.ProtectionReason, &s.ErrorCode, &s.ExpiresAt, &s.CreatedAt, &s.UpdatedAt)
	return s, e
}
func (a *App) consoleSession(ctx context.Context, id string, u User) (ConsoleSession, error) {
	s, e := scanConsole(a.DB.QueryRow(ctx, `SELECT `+consoleColumns+` FROM console_sessions WHERE id=$1 AND ($2='admin' OR actor_id=$3)`, id, u.Role, u.ID))
	if e != nil {
		return s, dbError(e)
	}
	if _, e = a.server(ctx, s.ServerID, u); e != nil {
		return s, e
	}
	return s, nil
}
func (a *App) registerConsole() {
	a.route("POST /api/v1/console-sessions/{id}/reconcile", true, a.reconcileMockConsole)
	a.route("GET /api/v1/console-sessions", false, func(w http.ResponseWriter, r *http.Request, u User) error {
		rows, e := a.DB.Query(r.Context(), `SELECT `+consoleColumns+` FROM console_sessions WHERE ($1='admin' OR (actor_id=$2 AND EXISTS(SELECT 1 FROM servers s WHERE s.id=console_sessions.server_id AND s.owner_id=$2))) ORDER BY created_at DESC LIMIT 200`, u.Role, u.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		out := []ConsoleSession{}
		for rows.Next() {
			s, e := scanConsole(rows)
			if e != nil {
				return e
			}
			out = append(out, s)
		}
		if e = rows.Err(); e != nil {
			return e
		}
		jsonResponse(w, 200, out)
		return nil
	})
	a.route("POST /api/v1/servers/{id}/console-sessions", false, a.requestConsole)
	a.route("GET /api/v1/console-sessions/{id}", false, func(w http.ResponseWriter, r *http.Request, u User) error {
		s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
		if e != nil {
			return e
		}
		jsonResponse(w, 200, s)
		return nil
	})
	a.route("POST /api/v1/console-sessions/{id}/renew", false, a.renewConsole)
	a.route("POST /api/v1/console-sessions/{id}/protection", false, a.protectConsole)
	a.route("DELETE /api/v1/console-sessions/{id}", false, a.releaseConsole)
	a.route("POST /api/v1/console-sessions/{id}/tickets", false, a.consoleTicket)
	a.route("POST /api/v1/console-sessions/{id}/redeem", false, a.redeemConsoleTicket)
	a.route("GET /api/v1/gateway/{id}/events", false, a.consoleEvents)
	a.route("POST /api/v1/gateway/{id}/input", false, a.consoleInput)
	a.route("GET /api/v1/console-sessions/{id}/media", false, a.listMedia)
	a.route("POST /api/v1/console-sessions/{id}/media", false, a.uploadMedia)
	a.route("POST /api/v1/console-sessions/{id}/media/{media}/mount", false, a.mountMedia)
	a.route("DELETE /api/v1/console-sessions/{id}/media/{media}/mount", false, a.unmountMedia)
	a.route("DELETE /api/v1/console-sessions/{id}/media/{media}", false, a.deleteMedia)
	a.route("GET /api/v1/gateway/{id}/{rest...}", false, func(w http.ResponseWriter, r *http.Request, u User) error {
		return fail(403, "CHANNEL_NOT_ALLOWED", "Only the audited mock event channel is enabled; real video/Janus/WebRTC are blocked")
	})
}
func (a *App) requestConsole(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "operator" {
		return fail(403, "FORBIDDEN", "Operator is read-only")
	}
	var body struct{}
	if e := decode(r, &body); e != nil {
		return e
	}
	s, e := a.server(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var owner string
	var maintenance bool
	var version int64
	if e = tx.QueryRow(ctx, `SELECT owner_id,maintenance,version FROM servers WHERE id=$1 FOR UPDATE`, s.ID).Scan(&owner, &maintenance, &version); e != nil {
		return e
	}
	if u.Role != "admin" && owner != u.ID {
		return fail(403, "FORBIDDEN", "Server ownership changed")
	}
	if maintenance {
		return fail(409, "MAINTENANCE", "Server under maintenance")
	}
	var inactive bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_mappings WHERE server_id=$1 AND state<>'active') AND NOT EXISTS(SELECT 1 FROM service_mappings WHERE server_id=$1 AND state='active')`, s.ID).Scan(&inactive); e != nil {
		return e
	}
	if inactive {
		return fail(403, "SERVICE_INACTIVE", "Service access is suspended or terminated")
	}
	var binding, device, kind string
	var port int
	var bv, dv int64
	var enabled bool
	e = tx.QueryRow(ctx, `SELECT b.id,b.device_id,b.port,b.version,d.version,d.kind,d.enabled FROM bindings b JOIN devices d ON d.id=b.device_id WHERE b.server_id=$1 AND b.kind='console' AND b.active`, s.ID).Scan(&binding, &device, &port, &bv, &dv, &kind, &enabled)
	if e != nil {
		return dbError(e)
	}
	if port < 1 || port > 16 {
		return fail(400, "INVALID_PORT", "Console port must be 1–16")
	}
	if !enabled {
		return fail(409, "DEVICE_DISABLED", "Device is disabled")
	}
	if kind != "mock_pikvm" {
		return fail(409, "ISOLATION_UNVERIFIED", "Real console channels are disabled until physical isolation is verified")
	}
	var existing string
	e = tx.QueryRow(ctx, `SELECT id FROM console_sessions WHERE actor_id=$1 AND server_id=$2 AND state<>'released'`, u.ID, s.ID).Scan(&existing)
	if e == nil {
		if e = tx.Commit(ctx); e != nil {
			return e
		}
		v, e := a.consoleSession(ctx, existing, u)
		if e != nil {
			return e
		}
		jsonResponse(w, 200, v)
		return nil
	}
	if e != pgx.ErrNoRows {
		return e
	}
	id := newID()
	_, e = tx.Exec(ctx, `INSERT INTO console_sessions(id,actor_id,server_id,device_id,binding_id,binding_version,server_version,device_version,port,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'queued',now()+interval '15 minutes')`, id, u.ID, s.ID, device, binding, bv, version, dv, port)
	if e != nil {
		return dbError(e)
	}
	if e = audit(ctx, tx, u, "console.request", id, map[string]string{"server_id": s.ID}); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if e = a.advanceConsole(ctx, id); e != nil {
		return e
	}
	v, e := a.consoleSession(ctx, id, u)
	if e != nil {
		return e
	}
	jsonResponse(w, 202, v)
	return nil
}
func (a *App) advanceConsole(ctx context.Context, id string) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var serverID string
	if e = tx.QueryRow(ctx, `SELECT server_id FROM console_sessions WHERE id=$1`, id).Scan(&serverID); e != nil {
		return e
	}
	var serverVersion int64
	if e = tx.QueryRow(ctx, `SELECT version FROM servers WHERE id=$1 FOR UPDATE`, serverID).Scan(&serverVersion); e != nil {
		return e
	}
	s, e := scanConsole(tx.QueryRow(ctx, `SELECT `+consoleColumns+` FROM console_sessions WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return e
	}
	if s.State != "queued" {
		return nil
	}
	var lease *string
	var blocked, kind, scenario string
	var cooldown *time.Time
	var dv int64
	var enabled bool
	if e = tx.QueryRow(ctx, `SELECT lease_owner,blocked_reason,kind,scenario,cooldown_until,version,enabled FROM devices WHERE id=$1 FOR UPDATE`, s.DeviceID).Scan(&lease, &blocked, &kind, &scenario, &cooldown, &dv, &enabled); e != nil {
		return e
	}
	if lease != nil || blocked != "" || (cooldown != nil && time.Now().Before(*cooldown)) {
		return nil
	}
	var first string
	if e = tx.QueryRow(ctx, `SELECT id FROM console_sessions WHERE device_id=$1 AND state='queued' ORDER BY created_at,id LIMIT 1`, s.DeviceID).Scan(&first); e != nil {
		return e
	}
	if first != id {
		return nil
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bindings b JOIN servers v ON v.id=b.server_id JOIN users u ON u.id=$6 WHERE b.id=$1 AND b.active AND b.version=$2 AND v.version=$3 AND NOT v.maintenance AND b.device_id=$4 AND b.port=$5 AND (u.role='admin' OR (u.role='customer' AND v.owner_id=u.id)) AND NOT EXISTS(SELECT 1 FROM service_mappings m WHERE m.server_id=v.id AND m.state<>'active' AND NOT EXISTS(SELECT 1 FROM service_mappings ma WHERE ma.server_id=v.id AND ma.state='active')))`, s.BindingID, s.BindingVersion, s.ServerVersion, s.DeviceID, s.Port, s.ActorID).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid || dv != s.DeviceVersion || !enabled {
		_, e = tx.Exec(ctx, `UPDATE console_sessions SET state='released',error_code='BINDING_CHANGED',updated_at=now() WHERE id=$1`, id)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if kind != "mock_pikvm" {
		return fail(409, "ISOLATION_UNVERIFIED", "Real console unavailable")
	}
	// Mock hardware has persistent state; a prior owner or residual media/input prevents assignment.
	var residue bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mock_console_state WHERE device_id=$1 AND (session_id IS NOT NULL OR media_id IS NOT NULL OR pressed_keys<>'[]'::jsonb)) OR EXISTS(SELECT 1 FROM gateway_connections g JOIN console_sessions c ON c.id=g.session_id WHERE c.device_id=$1)`, s.DeviceID).Scan(&residue); e != nil {
		return e
	}
	state, code := "active", ""
	if residue || scenario != "success" {
		state = "blocked"
		code = "CLEANUP_UNCONFIRMED"
	}
	if _, e = tx.Exec(ctx, `UPDATE devices SET lease_owner=$2,lease_kind='console',blocked_reason=$3,updated_at=now() WHERE id=$1`, s.DeviceID, id, code); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE console_sessions SET state=$2,error_code=$3,expires_at=now()+interval '15 minutes',updated_at=now() WHERE id=$1`, id, state, code); e != nil {
		return e
	}
	if state == "active" {
		if _, e = tx.Exec(ctx, `INSERT INTO mock_console_state(device_id,selected_port,session_id) VALUES($1,$2,$3) ON CONFLICT(device_id) DO UPDATE SET selected_port=$2,session_id=$3,frames=0,updated_at=now()`, s.DeviceID, s.Port, id); e != nil {
			return e
		}
	}
	if e = audit(ctx, tx, User{ID: s.ActorID}, "console.acquire", id, map[string]any{"state": state, "simulated": true, "target_port": s.Port, "generation": s.Generation}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) renewConsole(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "operator" {
		return fail(403, "FORBIDDEN", "Operator is read-only")
	}
	s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	tag, e := a.DB.Exec(r.Context(), `UPDATE console_sessions SET expires_at=now()+interval '15 minutes',updated_at=now() WHERE id=$1 AND (state='protected' OR (state='active' AND expires_at>now()))`, s.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fail(409, "SESSION_EXPIRED", "Session cannot be renewed")
	}
	v, e := a.consoleSession(r.Context(), s.ID, u)
	if e != nil {
		return e
	}
	jsonResponse(w, 200, v)
	return nil
}
func (a *App) protectConsole(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "operator" {
		return fail(403, "FORBIDDEN", "Operator is read-only")
	}
	var b struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	b.Reason = strings.TrimSpace(b.Reason)
	if b.Enabled && (len(b.Reason) < 3 || len(b.Reason) > 300) {
		return fail(400, "REASON_REQUIRED", "Provide a 3–300 character installation/BIOS reason")
	}
	s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	ctx := r.Context()
	tx, _, e := a.lockConsoleAuthority(r, u, s)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	state, reason := "active", ""
	if b.Enabled {
		state = "protected"
		reason = b.Reason
	}
	tag, e := tx.Exec(ctx, `UPDATE console_sessions SET state=$2,protection_reason=$3,expires_at=now()+interval '15 minutes',updated_at=now() WHERE id=$1 AND (state='protected' OR (state='active' AND expires_at>now()))`, s.ID, state, reason)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fail(409, "INVALID_STATE", "Active verified session required")
	}
	if e = audit(ctx, tx, u, "console.protection", s.ID, b); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	v, e := a.consoleSession(ctx, s.ID, u)
	if e != nil {
		return e
	}
	jsonResponse(w, 200, v)
	return nil
}
func (a *App) revokeConsole(ctx context.Context, id string, u User) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	s, e := scanConsole(tx.QueryRow(ctx, `SELECT `+consoleColumns+` FROM console_sessions WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return e
	}
	if s.State == "protected" {
		return fail(409, "PROTECTED_SESSION", "Explicitly end the protected installation/BIOS job before release")
	}
	if s.State == "released" {
		return nil
	}
	state := "revoking"
	if s.State == "queued" {
		state = "released"
	}
	if _, e = tx.Exec(ctx, `UPDATE console_sessions SET state=$2,generation=generation+1,updated_at=now() WHERE id=$1`, id, state); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM console_tickets WHERE session_id=$1;`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM console_access WHERE session_id=$1`, id); e != nil {
		return e
	}
	if e = audit(ctx, tx, u, "console.revoke", id, map[string]string{"state": state}); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	a.streamMu.Lock()
	for _, cancel := range a.streams[id] {
		cancel()
	}
	a.streamMu.Unlock()
	return a.finishConsole(ctx, id)
}
func (a *App) finishConsole(ctx context.Context, id string) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	s, e := scanConsole(tx.QueryRow(ctx, `SELECT `+consoleColumns+` FROM console_sessions WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return e
	}
	if s.State != "revoking" {
		return nil
	}
	var remaining int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_connections WHERE session_id=$1`, id).Scan(&remaining); e != nil {
		return e
	}
	if remaining > 0 {
		return nil
	}
	var scenario, kind string
	var lease *string
	if e = tx.QueryRow(ctx, `SELECT scenario,kind,lease_owner FROM devices WHERE id=$1 FOR UPDATE`, s.DeviceID).Scan(&scenario, &kind, &lease); e != nil {
		return e
	}
	if lease == nil || *lease != id {
		return fail(409, "LEASE_MISMATCH", "Device ownership needs reconciliation")
	}
	if scenario != "success" || kind != "mock_pikvm" {
		if _, e = tx.Exec(ctx, `UPDATE console_sessions SET state='blocked',error_code='CLEANUP_UNCONFIRMED',updated_at=now() WHERE id=$1`, id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE devices SET blocked_reason='CLEANUP_UNCONFIRMED' WHERE id=$1`, s.DeviceID); e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if _, e = tx.Exec(ctx, `UPDATE mock_console_state SET selected_port=NULL,session_id=NULL,pressed_keys='[]',media_id=NULL,frames=0,updated_at=now() WHERE device_id=$1`, s.DeviceID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE media_assets SET state='ready' WHERE session_id=$1 AND state='mounted'`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE console_sessions SET state='released',error_code='',updated_at=now() WHERE id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE devices SET lease_owner=NULL,lease_kind=NULL,blocked_reason='',updated_at=now() WHERE id=$1 AND lease_owner=$2`, s.DeviceID, id); e != nil {
		return e
	}
	if e = audit(ctx, tx, User{ID: s.ActorID}, "console.cleanup", id, map[string]any{"connections": 0, "keys_cleared": true, "media_detached": true, "target_cleared": true, "simulated": true}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) releaseConsole(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "operator" {
		return fail(403, "FORBIDDEN", "Operator is read-only")
	}
	s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	if e = a.revokeConsole(r.Context(), s.ID, u); e != nil {
		return e
	}
	v, e := a.consoleSession(r.Context(), s.ID, u)
	if e != nil {
		return e
	}
	jsonResponse(w, 202, v)
	return nil
}

// Reconciliation never expires protected jobs, never clears unknown connections, and never issues real device commands.
func (a *App) RunConsole(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.streamMu.Lock()
			for _, v := range a.streams {
				for _, cancel := range v {
					cancel()
				}
			}
			a.streamMu.Unlock()
			return
		case <-ticker.C:
			_ = a.ConsoleTick(ctx)
		}
	}
}
func (a *App) ConsoleTick(ctx context.Context) error {
	rows, e := a.DB.Query(ctx, `SELECT id,actor_id,state FROM console_sessions WHERE state IN ('queued','revoking') OR (state='active' AND expires_at<=now()) ORDER BY created_at,id`)
	if e != nil {
		return e
	}
	type item struct{ id, actor, state string }
	var items []item
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.actor, &v.state); e != nil {
			rows.Close()
			return e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range items {
		switch v.state {
		case "queued":
			e = a.advanceConsole(ctx, v.id)
		case "revoking":
			e = a.finishConsole(ctx, v.id)
		default:
			e = a.revokeConsole(ctx, v.id, User{ID: v.actor})
		}
		if e != nil {
			return e
		}
	}
	return nil
}

type consoleQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (a *App) verifiedConsole(ctx context.Context, id string, u User) (ConsoleSession, error) {
	return verifiedConsoleWith(ctx, a.DB, id, u)
}
func verifiedConsoleWith(ctx context.Context, q consoleQuerier, id string, u User) (ConsoleSession, error) {
	var currentRole string
	if e := q.QueryRow(ctx, `SELECT role FROM users WHERE id=$1`, u.ID).Scan(&currentRole); e != nil {
		return ConsoleSession{}, fail(403, "FORBIDDEN", "Identity no longer valid")
	}
	if currentRole != "admin" && currentRole != "customer" {
		return ConsoleSession{}, fail(403, "FORBIDDEN", "Console control not permitted")
	}
	u.Role = currentRole
	s, e := scanConsole(q.QueryRow(ctx, `SELECT `+consoleColumns+` FROM console_sessions WHERE id=$1 AND ($2='admin' OR actor_id=$3)`, id, u.Role, u.ID))
	if e != nil {
		return s, e
	}
	var valid bool
	e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM console_sessions c JOIN devices d ON d.id=c.device_id JOIN bindings b ON b.id=c.binding_id JOIN servers v ON v.id=c.server_id JOIN mock_console_state m ON m.device_id=d.id WHERE c.id=$1 AND ($2='admin' OR v.owner_id=$3) AND c.state IN ('active','protected') AND (c.state='protected' OR c.expires_at>now()) AND d.lease_owner=c.id AND d.lease_kind='console' AND d.blocked_reason='' AND d.kind='mock_pikvm' AND d.scenario='success' AND d.enabled AND d.version=c.device_version AND b.active AND b.version=c.binding_version AND v.version=c.server_version AND NOT v.maintenance AND m.session_id=c.id AND m.selected_port=c.port AND NOT EXISTS(SELECT 1 FROM service_mappings sm WHERE sm.server_id=v.id AND sm.state<>'active' AND NOT EXISTS(SELECT 1 FROM service_mappings ma WHERE ma.server_id=v.id AND ma.state='active')))`, id, u.Role, u.ID).Scan(&valid)
	if e != nil {
		return s, e
	}
	if !valid {
		return s, fail(409, "TARGET_UNVERIFIED", "Session, target, binding, or cleanup state is no longer verified")
	}
	return s, nil
}
func (a *App) consoleTicket(w http.ResponseWriter, r *http.Request, u User) error {
	s, e := a.verifiedConsole(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	token := newID()
	_, e = a.DB.Exec(r.Context(), `INSERT INTO console_tickets(token_hash,session_id,actor_id,generation,expires_at) VALUES($1,$2,$3,$4,now()+interval '30 seconds')`, tokenHash(token), s.ID, u.ID, s.Generation)
	if e != nil {
		return e
	}
	jsonResponse(w, 201, map[string]any{"ticket": token, "expires_in": 30})
	return nil
}
func (a *App) redeemConsoleTicket(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Ticket string `json:"ticket"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	s, e := a.verifiedConsole(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE console_tickets SET consumed=true WHERE token_hash=$1 AND session_id=$2 AND actor_id=$3 AND generation=$4 AND NOT consumed AND expires_at>now()`, tokenHash(b.Ticket), s.ID, u.ID, s.Generation)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return fail(403, "TICKET_INVALID", "Ticket expired, already consumed, or belongs to another session")
	}
	token := newID()
	if _, e = tx.Exec(ctx, `INSERT INTO console_access(token_hash,session_id,actor_id,generation,expires_at) VALUES($1,$2,$3,$4,now()+interval '5 minutes')`, tokenHash(token), s.ID, u.ID, s.Generation); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	jsonResponse(w, 201, map[string]any{"access_token": token, "expires_in": 300})
	return nil
}
func (a *App) gatewayAuth(r *http.Request, u User) (ConsoleSession, error) {
	if r.Header.Get("X-Portal-Token") != "" {
		current, id, e := a.portalAuth(r)
		if e != nil {
			return ConsoleSession{}, e
		}
		if current.ID != u.ID || id != r.PathValue("id") {
			return ConsoleSession{}, fail(403, "FORBIDDEN", "Portal scope changed")
		}
	} else {
		current, e := a.authenticate(r)
		if e != nil {
			return ConsoleSession{}, e
		}
		if current.ID != u.ID {
			return ConsoleSession{}, fail(403, "FORBIDDEN", "Identity changed")
		}
	}
	s, e := a.verifiedConsole(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return s, e
	}
	var valid bool
	e = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM console_access WHERE token_hash=$1 AND session_id=$2 AND actor_id=$3 AND generation=$4 AND expires_at>now())`, tokenHash(r.Header.Get("X-Console-Token")), s.ID, u.ID, s.Generation).Scan(&valid)
	if e != nil {
		return s, e
	}
	if !valid {
		return s, fail(403, "ACCESS_EXPIRED", "Console access token is invalid or expired")
	}
	return s, nil
}
func (a *App) consoleEvents(w http.ResponseWriter, r *http.Request, u User) error {
	s, e := a.gatewayAuth(r, u)
	if e != nil {
		return e
	}
	f, ok := w.(http.Flusher)
	if !ok {
		return fail(500, "STREAM_UNAVAILABLE", "Streaming unavailable")
	}
	id := newID()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if _, e = a.DB.Exec(ctx, `INSERT INTO gateway_connections(id,session_id,generation) VALUES($1,$2,$3)`, id, s.ID, s.Generation); e != nil {
		return e
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_, _ = a.DB.Exec(cleanup, `DELETE FROM gateway_connections WHERE id=$1`, id)
		a.streamMu.Lock()
		delete(a.streams[s.ID], id)
		a.streamMu.Unlock()
	}()
	a.streamMu.Lock()
	if a.streams[s.ID] == nil {
		a.streams[s.ID] = map[string]context.CancelFunc{}
	}
	a.streams[s.ID][id] = cancel
	a.streamMu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f.Flush()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, e := a.gatewayAuth(r, u)
			if e != nil || current.Generation != s.Generation {
				return nil
			}
			var frame int64
			e = a.DB.QueryRow(ctx, `UPDATE mock_console_state SET frames=frames+1,updated_at=now() WHERE device_id=$1 AND session_id=$2 AND selected_port=$3 RETURNING frames`, s.DeviceID, s.ID, s.Port).Scan(&frame)
			if e != nil {
				return nil
			}
			b, _ := json.Marshal(map[string]any{"simulated": true, "server_id": s.ServerID, "port": s.Port, "frame": frame, "generation": s.Generation, "observed_at": time.Now().UTC()})
			if _, e = fmt.Fprintf(w, "event: frame\ndata: %s\n\n", b); e != nil {
				return nil
			}
			f.Flush()
		}
	}
}
func (a *App) consoleInput(w http.ResponseWriter, r *http.Request, u User) error {
	s, e := a.gatewayAuth(r, u)
	if e != nil {
		return e
	}
	var b struct {
		Type    string `json:"type"`
		Key     string `json:"key"`
		Pressed bool   `json:"pressed"`
	}
	if e = decode(r, &b); e != nil {
		return e
	}
	allowed := map[string]bool{"Enter": true, "Escape": true, "ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true, "ControlLeft": true, "AltLeft": true, "Delete": true, "F2": true, "F12": true, "MouseLeft": true, "MouseRight": true, "KeyA": true}
	if b.Type != "key" || !allowed[b.Key] {
		return fail(400, "INPUT_NOT_ALLOWED", "Unsupported input event or key")
	}
	tx, _, e := a.lockConsoleMutation(r, u, s, true)
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var keys []string
	var raw []byte
	if e = tx.QueryRow(r.Context(), `SELECT pressed_keys FROM mock_console_state WHERE device_id=$1 AND session_id=$2 FOR UPDATE`, s.DeviceID, s.ID).Scan(&raw); e != nil {
		return e
	}
	if e = json.Unmarshal(raw, &keys); e != nil {
		return e
	}
	filtered := []string{}
	for _, k := range keys {
		if k != b.Key {
			filtered = append(filtered, k)
		}
	}
	if b.Pressed {
		filtered = append(filtered, b.Key)
	}
	encoded, _ := json.Marshal(filtered)
	if _, e = tx.Exec(r.Context(), `UPDATE mock_console_state SET pressed_keys=$3,updated_at=now() WHERE device_id=$1 AND session_id=$2`, s.DeviceID, s.ID, encoded); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 200, map[string]any{"simulated": true, "pressed_keys": filtered})
	return nil
}
