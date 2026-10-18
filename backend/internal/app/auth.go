package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"strings"
	"time"
)

type loginAttempt struct {
	Count int
	Until time.Time
}

func tokenHash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func hashPassword(v string) (string, error) {
	if len(v) < 12 || len(v) > 72 {
		return "", fail(400, "PASSWORD_POLICY", "Password must be 12–72 bytes")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(v), 12)
	return string(b), e
}
func (a *App) Bootstrap(ctx context.Context, email, password string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") || len(email) > 254 {
		return fail(400, "INVALID_EMAIL", "Valid email required")
	}
	h, e := hashPassword(password)
	if e != nil {
		return e
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(834920114)`); e != nil {
		return e
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return fail(409, "ALREADY_INITIALIZED", "Bootstrap is only allowed for an empty user database")
	}
	id := newID()
	if _, e = tx.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,'admin')`, id, email, h); e != nil {
		return e
	}
	if e = audit(ctx, tx, User{ID: id}, "identity.bootstrap", id, map[string]string{"role": "admin"}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (a *App) authenticate(r *http.Request) (User, error) {
	var u User
	c, e := r.Cookie("bp_session")
	if e != nil {
		return u, fail(401, "UNAUTHENTICATED", "Sign in required")
	}
	e = a.DB.QueryRow(r.Context(), `SELECT u.id,u.email,u.role FROM auth_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()`, tokenHash(c.Value)).Scan(&u.ID, &u.Email, &u.Role)
	if e != nil {
		return u, fail(401, "UNAUTHENTICATED", "Session expired or invalid")
	}
	return u, nil
}
func (a *App) checkCSRF(r *http.Request) error {
	if o := r.Header.Get("Origin"); o != "" && o != a.Config.BaseURL {
		return fail(403, "CSRF", "Origin rejected")
	}
	c, e := r.Cookie("bp_session")
	if e != nil {
		return fail(403, "CSRF", "Session required")
	}
	var csrf string
	e = a.DB.QueryRow(r.Context(), `SELECT csrf FROM auth_sessions WHERE token_hash=$1 AND expires_at>now()`, tokenHash(c.Value)).Scan(&csrf)
	if e != nil || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		return fail(403, "CSRF", "CSRF token required")
	}
	return nil
}
func (a *App) registerAuth() {
	a.Mux.HandleFunc("POST /api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		if e := a.login(w, r); e != nil {
			a.writeError(w, e)
		}
	})
	a.route("GET /api/v1/me", false, func(w http.ResponseWriter, r *http.Request, u User) error {
		c, _ := r.Cookie("bp_session")
		var csrf string
		if e := a.DB.QueryRow(r.Context(), `SELECT csrf FROM auth_sessions WHERE token_hash=$1`, tokenHash(c.Value)).Scan(&csrf); e != nil {
			return e
		}
		jsonResponse(w, 200, map[string]any{"user": u, "csrf": csrf})
		return nil
	})
	a.route("POST /api/v1/logout", false, func(w http.ResponseWriter, r *http.Request, u User) error {
		c, _ := r.Cookie("bp_session")
		_, e := a.DB.Exec(r.Context(), `DELETE FROM auth_sessions WHERE token_hash=$1`, tokenHash(c.Value))
		if e != nil {
			return e
		}
		http.SetCookie(w, &http.Cookie{Name: "bp_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode})
		jsonResponse(w, 200, map[string]bool{"logged_out": true})
		return nil
	})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	if o := r.Header.Get("Origin"); o != "" && o != a.Config.BaseURL {
		return fail(403, "CSRF", "Origin rejected")
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if e := decode(r, &b); e != nil {
		return e
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := host
	a.authMu.Lock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.After(v.Until) {
			delete(a.attempts, k)
		}
	}
	v := a.attempts[key]
	if v.Count >= 10 {
		a.authMu.Unlock()
		return fail(429, "RATE_LIMITED", "Too many login attempts; wait one minute")
	}
	v.Count++
	if v.Until.IsZero() {
		v.Until = now.Add(time.Minute)
	}
	a.attempts[key] = v
	a.authMu.Unlock()
	var u User
	var hash string
	e := a.DB.QueryRow(r.Context(), `SELECT id,email,role,password_hash FROM users WHERE email=$1`, strings.ToLower(strings.TrimSpace(b.Email))).Scan(&u.ID, &u.Email, &u.Role, &hash)
	if e != nil {
		hash = "$2a$12$7sA5WZg5MsQb0UYCvqEgAuw5W3c1n.28o6vZGlhyAZrXAX9UNb7Bi"
	}
	check := bcrypt.CompareHashAndPassword([]byte(hash), []byte(b.Password))
	if e != nil || check != nil {
		return fail(401, "INVALID_CREDENTIALS", "Invalid email or password")
	}
	token, csrf := newID(), newID()
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		return e
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `DELETE FROM auth_sessions WHERE expires_at<now()`); e != nil {
		return e
	}
	if _, e = tx.Exec(r.Context(), `INSERT INTO auth_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)`, tokenHash(token), u.ID, csrf, now.Add(a.Config.SessionTTL)); e != nil {
		return e
	}
	if e = audit(r.Context(), tx, u, "identity.login", u.ID, map[string]bool{"authenticated": true}); e != nil {
		return e
	}
	if e = tx.Commit(r.Context()); e != nil {
		return e
	}
	http.SetCookie(w, &http.Cookie{Name: "bp_session", Value: token, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: int(a.Config.SessionTTL.Seconds())})
	jsonResponse(w, 200, map[string]any{"user": u, "csrf": csrf})
	return nil
}
