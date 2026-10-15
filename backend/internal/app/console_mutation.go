package app

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

// Recheck authority after a potentially slow request body. Locks serialize writes with
// ownership/lifecycle changes, session revocation, logout and role changes.
func (a *App) lockConsoleAuthority(r *http.Request, u User, expected ConsoleSession) (pgx.Tx, ConsoleSession, error) {
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return nil, expected, e
	}
	failed := true
	defer func() {
		if failed {
			tx.Rollback(ctx)
		}
	}()
	var version int64
	var owner string
	if e = tx.QueryRow(ctx, `SELECT version,owner_id FROM servers WHERE id=$1 FOR UPDATE`, expected.ServerID).Scan(&version, &owner); e != nil {
		return nil, expected, e
	}
	s, e := scanConsole(tx.QueryRow(ctx, `SELECT `+consoleColumns+` FROM console_sessions WHERE id=$1 FOR UPDATE`, expected.ID))
	if e != nil {
		return nil, s, e
	}
	if s.Generation != expected.Generation {
		return nil, s, fail(409, "SESSION_CHANGED", "Session was revoked or changed while the request was in progress")
	}
	var role string
	if e = tx.QueryRow(ctx, `SELECT role FROM users WHERE id=$1 FOR SHARE`, u.ID).Scan(&role); e != nil {
		return nil, s, e
	}
	if role != "admin" && role != "customer" {
		return nil, s, fail(403, "FORBIDDEN", "Control permission was revoked")
	}
	if role != "admin" {
		if owner != u.ID || s.ActorID != u.ID {
			return nil, s, fail(403, "FORBIDDEN", "Session ownership changed")
		}
		inactive, e := powerServiceInactive(ctx, tx, s.ServerID)
		if e != nil {
			return nil, s, e
		}
		if inactive {
			return nil, s, fail(403, "SERVICE_INACTIVE", "Service access was revoked")
		}
	}
	if r.Header.Get("X-Portal-Token") != "" {
		var id string
		e = tx.QueryRow(ctx, `SELECT p.session_id FROM portal_grants p JOIN service_mappings m ON m.id=p.mapping_id JOIN integrations i ON i.id=m.integration_id WHERE p.token_hash=$1 AND p.kind='access' AND p.actor_id=$2 AND p.expires_at>now() AND m.state='active' AND i.enabled FOR SHARE OF p,m,i`, tokenHash(r.Header.Get("X-Portal-Token")), u.ID).Scan(&id)
		if e != nil || id != s.ID {
			return nil, s, fail(403, "PORTAL_ACCESS_EXPIRED", "Portal access was revoked")
		}
	} else {
		cookie, e := r.Cookie("bp_session")
		if e != nil {
			return nil, s, fail(401, "UNAUTHENTICATED", "Session required")
		}
		var id string
		e = tx.QueryRow(ctx, `SELECT user_id FROM auth_sessions WHERE token_hash=$1 AND expires_at>now() FOR SHARE`, tokenHash(cookie.Value)).Scan(&id)
		if e != nil || id != u.ID {
			return nil, s, fail(401, "UNAUTHENTICATED", "Sign-in session was revoked")
		}
	}
	failed = false
	return tx, s, nil
}
func (a *App) lockConsoleMutation(r *http.Request, u User, expected ConsoleSession, access bool) (pgx.Tx, ConsoleSession, error) {
	tx, s, e := a.lockConsoleAuthority(r, u, expected)
	if e != nil {
		return nil, s, e
	}
	ctx := r.Context()
	failed := true
	defer func() {
		if failed {
			tx.Rollback(ctx)
		}
	}()
	if (s.State != "active" && s.State != "protected") || (s.State == "active" && !s.ExpiresAt.After(time.Now())) {
		return nil, s, fail(409, "SESSION_CHANGED", "Session expired or was revoked")
	}
	if access {
		var id string
		e = tx.QueryRow(ctx, `SELECT session_id FROM console_access WHERE token_hash=$1 AND actor_id=$2 AND session_id=$3 AND generation=$4 AND expires_at>now() FOR SHARE`, tokenHash(r.Header.Get("X-Console-Token")), u.ID, s.ID, s.Generation).Scan(&id)
		if e != nil {
			return nil, s, fail(403, "ACCESS_EXPIRED", "Console access was revoked")
		}
	}
	// Inventory and lifecycle writers take the server lock; this verification remains stable until commit.
	if _, e = verifiedConsoleWith(ctx, tx, s.ID, u); e != nil {
		return nil, s, e
	}
	failed = false
	return tx, s, nil
}
