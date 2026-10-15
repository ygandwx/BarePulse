package app

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ygandwx/BarePulse/backend/migrations"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NOT_RUN: TEST_DATABASE_URL required for PostgreSQL integration")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + newID()
	if _, e = admin.Exec(ctx, `CREATE SCHEMA `+schema); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	conf, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	conf.ConnConfig.RuntimeParams["search_path"] = schema
	conf.MaxConns = 12
	db, e := pgxpool.NewWithConfig(ctx, conf)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	if e = migrations.Apply(ctx, db); e != nil {
		t.Fatal(e)
	}
	return New(db, Config{BaseURL: "http://localhost", MediaDir: t.TempDir(), MaxMediaBytes: 1024 * 1024})
}

// Fixtures all use the same documented test password. Hash it once at the
// production cost rather than spending most race-test time hashing duplicates.
// Authentication tests still exercise Bootstrap/login/password verification.
var fixturePasswordHash = sync.OnceValues(func() (string, error) { return hashPassword("test-password-123!") })

func testUser(t *testing.T, a *App, role string) (User, string, string) {
	t.Helper()
	u := User{ID: newID(), Email: newID() + "@example.test", Role: role}
	h, e := fixturePasswordHash()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec(context.Background(), `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,$4)`, u.ID, u.Email, h, u.Role); e != nil {
		t.Fatal(e)
	}
	token, csrf := newID(), newID()
	if _, e = a.DB.Exec(context.Background(), `INSERT INTO auth_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)`, tokenHash(token), u.ID, csrf, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	return u, csrf, token
}
func testRequest(t *testing.T, a *App, method, path string, body any, u User) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if e := json.NewEncoder(&buf).Encode(body); e != nil {
			t.Fatal(e)
		}
	}
	r := httptest.NewRequest(method, path, &buf)
	r.Header.Set("Content-Type", "application/json")
	if u.ID != "" {
		token, csrf := newID(), newID()
		if _, e := a.DB.Exec(context.Background(), `INSERT INTO auth_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,$4)`, tokenHash(token), u.ID, csrf, time.Now().Add(time.Hour)); e != nil {
			t.Fatal(e)
		}
		r.AddCookie(&http.Cookie{Name: "bp_session", Value: token})
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}
func TestAuthBootstrapLoginAndCSRF(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if e := a.Bootstrap(ctx, "admin@example.test", "test-password-123!"); e != nil {
		t.Fatal(e)
	}
	if e := a.Bootstrap(ctx, "other@example.test", "test-password-123!"); e == nil {
		t.Fatal("duplicate bootstrap accepted")
	}
	w := testRequest(t, a, "POST", "/api/v1/login", map[string]string{"email": "admin@example.test", "password": "test-password-123!"}, User{})
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	var b struct {
		User User   `json:"user"`
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &b)
	if b.CSRF == "" {
		t.Fatal("missing csrf")
	}
	r := httptest.NewRequest("POST", "/api/v1/logout", nil)
	r.AddCookie(w.Result().Cookies()[0])
	bad := httptest.NewRecorder()
	a.Handler().ServeHTTP(bad, r)
	if bad.Code != 403 {
		t.Fatalf("missing CSRF %d", bad.Code)
	}
	r = httptest.NewRequest("POST", "/api/v1/logout", nil)
	r.AddCookie(w.Result().Cookies()[0])
	r.Header.Set("X-CSRF-Token", b.CSRF)
	good := httptest.NewRecorder()
	a.Handler().ServeHTTP(good, r)
	if good.Code != 200 {
		t.Fatalf("logout %d", good.Code)
	}
	r = httptest.NewRequest("GET", "/api/v1/me", nil)
	r.AddCookie(w.Result().Cookies()[0])
	expired := httptest.NewRecorder()
	a.Handler().ServeHTTP(expired, r)
	if expired.Code != 401 {
		t.Fatal("logout cookie remained valid")
	}
}
