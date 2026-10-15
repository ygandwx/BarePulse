package app

import (
	"net/http"
)

func (a *App) registerPortal() {
	a.Mux.HandleFunc("POST /api/v1/portal/redeem", func(w http.ResponseWriter, r *http.Request) {
		if e := a.redeemPortal(w, r); e != nil {
			a.writeError(w, e)
		}
	})
	register := func(pattern string, h func(http.ResponseWriter, *http.Request, User) error) {
		a.Mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			u, id, e := a.portalAuth(r)
			if e == nil {
				r.SetPathValue("id", id)
				e = h(w, r, u)
			}
			if e != nil {
				a.writeError(w, e)
			}
		})
	}
	register("GET /api/v1/portal/session", func(w http.ResponseWriter, r *http.Request, u User) error {
		s, e := a.consoleSession(r.Context(), r.PathValue("id"), u)
		if e != nil {
			return e
		}
		jsonResponse(w, 200, s)
		return nil
	})
	register("POST /api/v1/portal/tickets", a.consoleTicket)
	register("POST /api/v1/portal/access", a.redeemConsoleTicket)
	register("POST /api/v1/portal/renew", a.renewConsole)
	register("POST /api/v1/portal/protection", a.protectConsole)
	register("DELETE /api/v1/portal/session", a.releaseConsole)
	register("GET /api/v1/portal/events", a.consoleEvents)
	register("POST /api/v1/portal/input", a.consoleInput)
	register("GET /api/v1/portal/media", a.listMedia)
	register("POST /api/v1/portal/media", a.uploadMedia)
	register("POST /api/v1/portal/media/{media}/mount", a.mountMedia)
	register("DELETE /api/v1/portal/media/{media}/mount", a.unmountMedia)
	register("DELETE /api/v1/portal/media/{media}", a.deleteMedia)
}
func (a *App) redeemPortal(w http.ResponseWriter, r *http.Request) error {
	if o := r.Header.Get("Origin"); o != "" && o != a.Config.BaseURL {
		return fail(403, "ORIGIN_REJECTED", "Origin rejected")
	}
	var b struct {
		Ticket string `json:"ticket"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	var mapping, session, actor string
	var integration string
	// Serialize ticket redemption with disabling/rotating the integration. This
	// prevents an access grant being inserted just after the revocation DELETE.
	if e = tx.QueryRow(r.Context(), `SELECT i.id FROM portal_grants p JOIN service_mappings m ON m.id=p.mapping_id JOIN integrations i ON i.id=m.integration_id WHERE p.token_hash=$1 AND i.enabled FOR SHARE OF i`, tokenHash(b.Ticket)).Scan(&integration); e != nil {
		return fail(403, "PORTAL_TICKET_INVALID", "Portal ticket is invalid or revoked")
	}
	e = tx.QueryRow(r.Context(), `UPDATE portal_grants p SET consumed=true FROM service_mappings m,servers s,integrations i,users u WHERE p.token_hash=$1 AND p.kind='ticket' AND NOT p.consumed AND p.expires_at>now() AND m.id=p.mapping_id AND m.state='active' AND s.id=m.server_id AND s.owner_id=m.owner_id AND i.id=m.integration_id AND i.enabled AND u.id=p.actor_id AND u.id=m.owner_id AND u.role IN ('admin','customer') RETURNING p.mapping_id,p.session_id,p.actor_id`, tokenHash(b.Ticket)).Scan(&mapping, &session, &actor)
	if e != nil {
		return fail(403, "PORTAL_TICKET_INVALID", "Portal ticket is invalid, consumed, or expired")
	}
	token := newID()
	if _, e = tx.Exec(r.Context(), `INSERT INTO portal_grants(token_hash,mapping_id,session_id,actor_id,kind,expires_at) VALUES($1,$2,$3,$4,'access',now()+interval '15 minutes')`, tokenHash(token), mapping, session, actor); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	jsonResponse(w, 201, map[string]any{"portal_token": token, "expires_in": 900, "session_id": session})
	return nil
}
func (a *App) portalAuth(r *http.Request) (User, string, error) {
	var u User
	var session string
	if o := r.Header.Get("Origin"); o != "" && o != a.Config.BaseURL {
		return u, "", fail(403, "ORIGIN_REJECTED", "Origin rejected")
	}
	e := a.DB.QueryRow(r.Context(), `SELECT u.id,u.email,u.role,p.session_id FROM portal_grants p JOIN service_mappings m ON m.id=p.mapping_id JOIN servers s ON s.id=m.server_id JOIN users u ON u.id=p.actor_id JOIN integrations i ON i.id=m.integration_id WHERE p.token_hash=$1 AND p.kind='access' AND p.expires_at>now() AND m.state='active' AND i.enabled AND s.owner_id=m.owner_id AND p.actor_id=m.owner_id`, tokenHash(r.Header.Get("X-Portal-Token"))).Scan(&u.ID, &u.Email, &u.Role, &session)
	if e != nil {
		return u, "", fail(403, "PORTAL_ACCESS_EXPIRED", "Return to your WHMCS service to request a new console entry")
	}
	if u.Role != "customer" && u.Role != "admin" {
		return u, "", fail(403, "FORBIDDEN", "Identity is no longer permitted to control a console")
	}
	u.Role = "customer"
	return u, session, nil
}
