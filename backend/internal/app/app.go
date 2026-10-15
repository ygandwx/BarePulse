package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ygandwx/BarePulse/frontend"
)

type Config struct {
	PowerProfiles     map[string]PowerProfile
	BaseURL           string
	SecureCookies     bool
	AllowRealDevices  bool
	AllowDeviceWrites bool
	SessionTTL        time.Duration
	MediaDir          string
	MaxMediaBytes     int64
}
type App struct {
	DB       *pgxpool.Pool
	Config   Config
	Mux      *http.ServeMux
	authMu   sync.Mutex
	attempts map[string]loginAttempt
	streamMu sync.Mutex
	streams  map[string]map[string]context.CancelFunc
}
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}
type apiError struct {
	Status        int
	Code, Message string
}

func (e *apiError) Error() string                 { return e.Code + ": " + e.Message }
func fail(status int, code, message string) error { return &apiError{status, code, message} }
func newID() string {
	var b [24]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func decode(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fail(415, "CONTENT_TYPE", "Use application/json")
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fail(400, "INVALID_JSON", "Invalid or unknown JSON fields")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fail(400, "INVALID_JSON", "One JSON object required")
	}
	return nil
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func dbError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "NOT_FOUND", "Resource not found")
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505", "23P01":
			return fail(409, "CONFLICT", "An active binding or unique value conflicts")
		case "23503":
			return fail(409, "REFERENCED_RESOURCE", "Referenced resource missing or still in use")
		case "23514", "22P02", "22003":
			return fail(400, "INVALID_VALUE", "Value violates data constraints")
		case "40001", "40P01":
			return fail(409, "CONCURRENT_CHANGE", "Concurrent change; reload before retrying")
		}
	}
	return err
}
func audit(ctx context.Context, tx pgx.Tx, u User, action, object string, details any) error {
	b, e := json.Marshal(details)
	if e != nil {
		return e
	}
	var actor any
	if u.ID != "" {
		actor = u.ID
	}
	_, e = tx.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,object_id,details) VALUES($1,$2,$3,$4,$5)`, newID(), actor, action, object, b)
	return e
}
func New(db *pgxpool.Pool, c Config) *App {
	if c.SessionTTL == 0 {
		c.SessionTTL = 8 * time.Hour
	}
	if c.MaxMediaBytes == 0 {
		c.MaxMediaBytes = 2_000_000_000
	}
	a := &App{DB: db, Config: c, Mux: http.NewServeMux(), attempts: make(map[string]loginAttempt), streams: make(map[string]map[string]context.CancelFunc)}
	a.registerAuth()
	a.registerInventory()
	a.registerOperations()
	a.registerTraffic()
	a.registerConsole()
	a.registerWHMCS()
	a.Mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if db.Ping(ctx) != nil {
			jsonResponse(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		jsonResponse(w, 200, map[string]any{"status": "ok", "real_devices": c.AllowRealDevices, "device_writes": c.AllowDeviceWrites})
	})
	a.Mux.Handle("GET /", http.FileServerFS(frontend.Files))
	return a
}
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Request-ID", newID())
		a.Mux.ServeHTTP(w, r)
	})
}
func (a *App) writeError(w http.ResponseWriter, err error) {
	err = dbError(err)
	var ae *apiError
	if !errors.As(err, &ae) {
		slog.Error("request failed", "error_type", fmt.Sprintf("%T", err))
		ae = &apiError{500, "INTERNAL_ERROR", "Request failed; use request_id for support"}
	}
	jsonResponse(w, ae.Status, map[string]any{"error": map[string]string{"code": ae.Code, "message": ae.Message}, "request_id": w.Header().Get("X-Request-ID")})
}
func (a *App) route(pattern string, admin bool, h func(http.ResponseWriter, *http.Request, User) error) {
	a.Mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		u, e := a.authenticate(r)
		if e == nil && admin && u.Role != "admin" {
			e = fail(403, "FORBIDDEN", "Administrator access required")
		}
		if e == nil && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			e = a.checkCSRF(r)
		}
		if e == nil {
			e = h(w, r, u)
		}
		if e != nil {
			a.writeError(w, e)
		}
	})
}
func (a *App) server(ctx context.Context, id string, u User) (Server, error) {
	var s Server
	e := a.DB.QueryRow(ctx, `SELECT id,name,owner_id,model,notes,maintenance,version,created_at,updated_at FROM servers WHERE id=$1 AND ($2='admin' OR (owner_id=$3 AND NOT EXISTS(SELECT 1 FROM service_mappings m WHERE m.server_id=servers.id AND m.state<>'active' AND NOT EXISTS(SELECT 1 FROM service_mappings ma WHERE ma.server_id=servers.id AND ma.state='active'))))`, id, u.Role, u.ID).Scan(&s.ID, &s.Name, &s.OwnerID, &s.Model, &s.Notes, &s.Maintenance, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	return s, dbError(e)
}
