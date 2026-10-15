package app

import "net/http"

// Reconciliation only resets our simulated device. Unknown real channels have no bypass endpoint.
func (a *App) reconcileMockConsole(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Reason             string `json:"reason"`
		ConnectionsStopped bool   `json:"connections_stopped"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	if len(b.Reason) < 8 || len(b.Reason) > 300 || !b.ConnectionsStopped {
		return fail(400, "REASON_REQUIRED", "Confirm connections stopped and provide an 8–300 character reconciliation reason")
	}
	s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var state string
	if e = tx.QueryRow(r.Context(), `SELECT state FROM console_sessions WHERE id=$1 FOR UPDATE`, s.ID).Scan(&state); e != nil {
		return e
	}
	if state != "blocked" && state != "revoking" {
		return fail(409, "INVALID_STATE", "Only blocked or revoking simulations may be reconciled")
	}
	var count int
	if e = tx.QueryRow(r.Context(), `SELECT count(*) FROM gateway_connections WHERE session_id=$1`, s.ID).Scan(&count); e != nil {
		return e
	}
	if count != 0 {
		return fail(409, "CONNECTIONS_REMAIN", "Old connections are still registered; reconciliation is blocked")
	}
	var kind string
	var lease *string
	if e = tx.QueryRow(r.Context(), `SELECT kind,lease_owner FROM devices WHERE id=$1 FOR UPDATE`, s.DeviceID).Scan(&kind, &lease); e != nil {
		return e
	}
	if kind != "mock_pikvm" || lease == nil || *lease != s.ID {
		return fail(409, "RECONCILIATION_DENIED", "This is not the owned mock device")
	}
	if _, e = tx.Exec(r.Context(), `UPDATE devices SET scenario='success' WHERE id=$1`, s.DeviceID); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `UPDATE console_sessions SET state='revoking',generation=generation+1,updated_at=now() WHERE id=$1`, s.ID); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "console.mock_reconcile", s.ID, b); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	if e = a.finishConsole(r.Context(), s.ID); e != nil {
		return e
	}
	v, e := a.consoleSession(r.Context(), s.ID, u)
	if e != nil {
		return e
	}
	jsonResponse(w, 200, v)
	return nil
}
