package app

import (
	"context"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Server struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerID     string    `json:"owner_id"`
	Model       string    `json:"model"`
	Notes       string    `json:"notes"`
	Maintenance bool      `json:"maintenance"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Device struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Address       string     `json:"address,omitempty"`
	Scenario      string     `json:"scenario"`
	Enabled       bool       `json:"enabled"`
	Version       int64      `json:"version"`
	LeaseKind     *string    `json:"lease_kind"`
	BlockedReason string     `json:"blocked_reason"`
	CooldownUntil *time.Time `json:"cooldown_until"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Binding struct {
	ID        string     `json:"id"`
	ServerID  string     `json:"server_id"`
	DeviceID  string     `json:"device_id"`
	Kind      string     `json:"kind"`
	Port      int        `json:"port"`
	Version   int64      `json:"version"`
	Active    bool       `json:"active"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

const serverColumns = "id,name,owner_id,model,notes,maintenance,version,created_at,updated_at"
const deviceColumns = "id,name,kind,address,scenario,enabled,version,lease_kind,blocked_reason,cooldown_until,created_at,updated_at"
const bindingColumns = "b.id,b.server_id,b.device_id,b.kind,b.port,b.version,b.active,b.created_at,b.ended_at"

func scanServer(row pgx.Row) (Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.Name, &s.OwnerID, &s.Model, &s.Notes, &s.Maintenance, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}
func scanDevice(row pgx.Row) (Device, error) {
	var d Device
	err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Address, &d.Scenario, &d.Enabled, &d.Version, &d.LeaseKind, &d.BlockedReason, &d.CooldownUntil, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}
func scanBinding(row pgx.Row) (Binding, error) {
	var b Binding
	err := row.Scan(&b.ID, &b.ServerID, &b.DeviceID, &b.Kind, &b.Port, &b.Version, &b.Active, &b.CreatedAt, &b.EndedAt)
	return b, err
}

func (a *App) registerInventory() {
	a.route("GET /api/v1/servers", false, a.listServers)
	a.route("GET /api/v1/servers/{id}", false, a.getServer)
	a.route("POST /api/v1/servers", true, a.saveServer)
	a.route("PUT /api/v1/servers/{id}", true, a.saveServer)
	a.route("DELETE /api/v1/servers/{id}", true, a.deleteServer)
	a.route("GET /api/v1/devices", true, a.listDevices)
	a.route("POST /api/v1/devices", true, a.saveDevice)
	a.route("PUT /api/v1/devices/{id}", true, a.saveDevice)
	a.route("DELETE /api/v1/devices/{id}", true, a.deleteDevice)
	a.route("GET /api/v1/bindings", false, a.listBindings)
	a.route("POST /api/v1/bindings", true, a.saveBinding)
	a.route("DELETE /api/v1/bindings/{id}", true, a.endBinding)
	a.route("GET /api/v1/users", true, a.listUsers)
	a.route("POST /api/v1/users", true, a.saveUser)
	a.route("PUT /api/v1/users/{id}", true, a.saveUser)
	a.route("DELETE /api/v1/users/{id}", true, a.deleteUser)
}

func (a *App) listServers(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE $1='admin' OR owner_id=$2 ORDER BY name", u.Role, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Server{}
	for rows.Next() {
		s, e := scanServer(rows)
		if e != nil {
			return e
		}
		items = append(items, s)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, items)
	return nil
}
func (a *App) getServer(w http.ResponseWriter, r *http.Request, u User) error {
	s, err := a.server(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	jsonResponse(w, 200, s)
	return nil
}
func validText(v string, max int) bool { return strings.TrimSpace(v) != "" && len(v) <= max }

func (a *App) saveServer(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Name        string `json:"name"`
		OwnerID     string `json:"owner_id"`
		Model       string `json:"model"`
		Notes       string `json:"notes"`
		Maintenance bool   `json:"maintenance"`
		Version     int64  `json:"version"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	if !validText(b.Name, 200) || b.OwnerID == "" || len(b.Model) > 500 || len(b.Notes) > 8000 {
		return fail(400, "INVALID_ASSET", "Name, owner, model or notes is invalid")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var owner string
	if err = tx.QueryRow(r.Context(), "SELECT id FROM users WHERE id=$1 FOR KEY SHARE", b.OwnerID).Scan(&owner); err != nil {
		return dbError(err)
	}
	id := r.PathValue("id")
	status := 200
	action := "server.updated"
	if id == "" {
		id = newID()
		status = 201
		action = "server.created"
		_, err = tx.Exec(r.Context(), "INSERT INTO servers(id,name,owner_id,model,notes,maintenance) VALUES($1,$2,$3,$4,$5,$6)", id, b.Name, b.OwnerID, b.Model, b.Notes, b.Maintenance)
	} else {
		old, e := scanServer(tx.QueryRow(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE id=$1 FOR UPDATE", id))
		if e != nil {
			return dbError(e)
		}
		if old.Version != b.Version {
			return fail(409, "STATE_STALE", "Reload the server before updating")
		}
		if e = ensureServerIdle(r.Context(), tx, id); e != nil {
			return e
		}
		_, err = tx.Exec(r.Context(), "UPDATE servers SET name=$2,owner_id=$3,model=$4,notes=$5,maintenance=$6,version=version+1,updated_at=now() WHERE id=$1", id, b.Name, b.OwnerID, b.Model, b.Notes, b.Maintenance)
	}
	if err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, action, id, map[string]any{"name": b.Name, "owner_id": b.OwnerID, "maintenance": b.Maintenance}); err != nil {
		return err
	}
	s, err := scanServer(tx.QueryRow(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE id=$1", id))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return dbError(err)
	}
	jsonResponse(w, status, s)
	return nil
}

// Active device leases protect assets against re-assignment while an old actor may still be executing.
func ensureServerIdle(ctx context.Context, tx pgx.Tx, id string) error {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bindings b JOIN devices d ON d.id=b.device_id WHERE b.server_id=$1 AND b.active AND d.lease_owner IS NOT NULL) OR EXISTS(SELECT 1 FROM operation_tasks WHERE server_id=$1 AND status IN ('running','verifying','unknown'))`, id).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return fail(409, "DEVICE_BUSY", "Server has an active or unresolved device operation/session")
	}
	return nil
}
func (a *App) deleteServer(w http.ResponseWriter, r *http.Request, u User) error {
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("id")
	_, err = scanServer(tx.QueryRow(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return dbError(err)
	}
	if err = ensureServerIdle(r.Context(), tx, id); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM servers WHERE id=$1", id); err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, "server.deleted", id, nil); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (a *App) listDevices(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), "SELECT "+deviceColumns+" FROM devices ORDER BY name")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Device{}
	for rows.Next() {
		d, e := scanDevice(rows)
		if e != nil {
			return e
		}
		items = append(items, d)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, items)
	return nil
}
func validDevice(kind, scenario string) bool {
	kinds := map[string]bool{"mock_pikvm": true, "mock_pdu": true, "mock_bmc": true, "pikvm": true, "apc": true, "redfish": true, "ipmi": true, "snmp": true}
	scenarios := map[string]bool{"success": true, "offline": true, "inner_error": true, "timeout": true, "busy": true}
	return kinds[kind] && scenarios[scenario]
}
func (a *App) saveDevice(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		Address       string `json:"address"`
		CredentialRef string `json:"credential_ref"`
		Scenario      string `json:"scenario"`
		Enabled       bool   `json:"enabled"`
		Version       int64  `json:"version"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Scenario == "" {
		b.Scenario = "success"
	}
	if !validText(b.Name, 200) || !validDevice(b.Kind, b.Scenario) || len(b.Address) > 2000 || len(b.CredentialRef) > 200 {
		return fail(400, "INVALID_DEVICE", "Invalid device name, kind, scenario or credential reference")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("id")
	status := 200
	action := "device.updated"
	if id == "" {
		id = newID()
		status = 201
		action = "device.created"
		_, err = tx.Exec(r.Context(), "INSERT INTO devices(id,name,kind,address,credential_ref,scenario,enabled) VALUES($1,$2,$3,$4,$5,$6,$7)", id, b.Name, b.Kind, b.Address, b.CredentialRef, b.Scenario, b.Enabled)
	} else {
		var version int64
		var lease *string
		var kind string
		if err = tx.QueryRow(r.Context(), "SELECT version,lease_owner,kind FROM devices WHERE id=$1 FOR UPDATE", id).Scan(&version, &lease, &kind); err != nil {
			return dbError(err)
		}
		if version != b.Version {
			return fail(409, "STATE_STALE", "Reload the device before updating")
		}
		if lease != nil {
			return fail(409, "DEVICE_BUSY", "Device lease must be safely released before editing")
		}
		if b.Kind != kind {
			var bound bool
			if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM bindings WHERE device_id=$1 AND active)", id).Scan(&bound); err != nil {
				return err
			}
			if bound {
				return fail(409, "BOUND_DEVICE", "End active bindings before changing device kind")
			}
		}
		_, err = tx.Exec(r.Context(), "UPDATE devices SET name=$2,kind=$3,address=$4,credential_ref=CASE WHEN $5='' THEN credential_ref ELSE $5 END,scenario=$6,enabled=$7,version=version+1,updated_at=now() WHERE id=$1", id, b.Name, b.Kind, b.Address, b.CredentialRef, b.Scenario, b.Enabled)
	}
	if err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, action, id, map[string]any{"name": b.Name, "kind": b.Kind, "enabled": b.Enabled, "scenario": b.Scenario}); err != nil {
		return err
	}
	d, err := scanDevice(tx.QueryRow(r.Context(), "SELECT "+deviceColumns+" FROM devices WHERE id=$1", id))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, status, d)
	return nil
}
func (a *App) deleteDevice(w http.ResponseWriter, r *http.Request, u User) error {
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("id")
	var lease *string
	if err = tx.QueryRow(r.Context(), "SELECT lease_owner FROM devices WHERE id=$1 FOR UPDATE", id).Scan(&lease); err != nil {
		return dbError(err)
	}
	if lease != nil {
		return fail(409, "DEVICE_BUSY", "Device has an unresolved lease")
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM devices WHERE id=$1", id); err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, "device.deleted", id, nil); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (a *App) listBindings(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), "SELECT "+bindingColumns+" FROM bindings b JOIN servers s ON s.id=b.server_id WHERE ($1='admin' OR s.owner_id=$2) AND ($3='' OR b.server_id=$3) ORDER BY b.created_at DESC", u.Role, u.ID, r.URL.Query().Get("server_id"))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Binding{}
	for rows.Next() {
		b, e := scanBinding(rows)
		if e != nil {
			return e
		}
		items = append(items, b)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, items)
	return nil
}
func validateBinding(kind, deviceKind string, port int) error {
	if port < 1 {
		return fail(400, "INVALID_PORT", "Port must be a positive integer")
	}
	if (deviceKind == "pikvm" || deviceKind == "mock_pikvm") && port > 16 {
		return fail(400, "INVALID_PORT", "PiKVM ports are integers from 1 to 16")
	}
	if (deviceKind == "redfish" || deviceKind == "ipmi" || deviceKind == "mock_bmc") && port != 1 {
		return fail(400, "INVALID_PORT", "A BMC represents one server; use port 1")
	}
	switch kind {
	case "console":
		if deviceKind != "pikvm" && deviceKind != "mock_pikvm" {
			return fail(400, "CAPABILITY_UNSUPPORTED", "Console binding requires a PiKVM device")
		}
	case "power":
		if deviceKind == "snmp" {
			return fail(400, "CAPABILITY_UNSUPPORTED", "SNMP traffic devices do not provide power control")
		}
	case "network":
		if deviceKind != "snmp" {
			return fail(400, "CAPABILITY_UNSUPPORTED", "Network binding requires an SNMP device")
		}
	default:
		return fail(400, "INVALID_BINDING", "Unknown binding kind")
	}
	return nil
}
func (a *App) saveBinding(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		ServerID  string `json:"server_id"`
		DeviceID  string `json:"device_id"`
		Kind      string `json:"kind"`
		Port      int    `json:"port"`
		ReplaceID string `json:"replace_id"`
		Version   int64  `json:"version"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err = scanServer(tx.QueryRow(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE id=$1 FOR UPDATE", b.ServerID)); err != nil {
		return dbError(err)
	}
	if err = ensureServerIdle(r.Context(), tx, b.ServerID); err != nil {
		return err
	}
	var deviceKind string
	var lease *string
	if err = tx.QueryRow(r.Context(), "SELECT kind,lease_owner FROM devices WHERE id=$1 FOR UPDATE", b.DeviceID).Scan(&deviceKind, &lease); err != nil {
		return dbError(err)
	}
	if lease != nil {
		return fail(409, "DEVICE_BUSY", "Device is held by an active or unresolved operation/session")
	}
	if err = validateBinding(b.Kind, deviceKind, b.Port); err != nil {
		return err
	}
	// Power and console views of one physical KVM port may share only the same server.
	var conflict bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM bindings WHERE device_id=$1 AND port=$2 AND active AND kind IN ('power','console') AND server_id<>$3)", b.DeviceID, b.Port, b.ServerID).Scan(&conflict); err != nil {
		return err
	}
	if conflict && b.Kind != "network" {
		return fail(409, "BINDING_CONFLICT", "Physical device port is already assigned to another server")
	}
	version := int64(1)
	if b.ReplaceID != "" {
		old, e := scanBinding(tx.QueryRow(r.Context(), "SELECT "+bindingColumns+" FROM bindings b WHERE b.id=$1 FOR UPDATE", b.ReplaceID))
		if e != nil {
			return dbError(e)
		}
		if old.ServerID != b.ServerID || old.Kind != b.Kind || !old.Active || old.Version != b.Version {
			return fail(409, "BINDING_CHANGED", "Replacement must match the active server binding and its version")
		}
		if _, err = tx.Exec(r.Context(), "UPDATE bindings SET active=false,ended_at=now() WHERE id=$1", old.ID); err != nil {
			return err
		}
		version = old.Version + 1
	}
	id := newID()
	if _, err = tx.Exec(r.Context(), "INSERT INTO bindings(id,server_id,device_id,kind,port,version) VALUES($1,$2,$3,$4,$5,$6)", id, b.ServerID, b.DeviceID, b.Kind, b.Port, version); err != nil {
		return dbError(err)
	}
	if _, err = tx.Exec(r.Context(), "UPDATE servers SET version=version+1,updated_at=now() WHERE id=$1", b.ServerID); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "binding.created", id, map[string]any{"server_id": b.ServerID, "device_id": b.DeviceID, "kind": b.Kind, "port": b.Port, "replaces": b.ReplaceID}); err != nil {
		return err
	}
	result, err := scanBinding(tx.QueryRow(r.Context(), "SELECT "+bindingColumns+" FROM bindings b WHERE b.id=$1", id))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return dbError(err)
	}
	jsonResponse(w, 201, result)
	return nil
}
func (a *App) endBinding(w http.ResponseWriter, r *http.Request, u User) error {
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("id")
	b, err := scanBinding(tx.QueryRow(r.Context(), "SELECT "+bindingColumns+" FROM bindings b WHERE b.id=$1", id))
	if err != nil {
		return dbError(err)
	}
	if _, err = scanServer(tx.QueryRow(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE id=$1 FOR UPDATE", b.ServerID)); err != nil {
		return err
	}
	if err = ensureServerIdle(r.Context(), tx, b.ServerID); err != nil {
		return err
	}
	var lease *string
	if err = tx.QueryRow(r.Context(), "SELECT lease_owner FROM devices WHERE id=$1 FOR UPDATE", b.DeviceID).Scan(&lease); err != nil {
		return err
	}
	if lease != nil {
		return fail(409, "DEVICE_BUSY", "Device is leased")
	}
	if _, err = tx.Exec(r.Context(), "UPDATE bindings SET active=false,ended_at=COALESCE(ended_at,now()) WHERE id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "UPDATE servers SET version=version+1,updated_at=now() WHERE id=$1", b.ServerID); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "binding.ended", id, nil); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (a *App) listUsers(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), "SELECT id,email,role FROM users ORDER BY email")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []User{}
	for rows.Next() {
		var user User
		if err = rows.Scan(&user.ID, &user.Email, &user.Role); err != nil {
			return err
		}
		items = append(items, user)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, items)
	return nil
}
func (a *App) saveUser(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Email    string `json:"email"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	parsed, e := mail.ParseAddress(b.Email)
	if e != nil || parsed.Address != b.Email || len(b.Email) > 254 || (b.Role != "admin" && b.Role != "customer" && b.Role != "operator") {
		return fail(400, "INVALID_USER", "Valid email and role are required")
	}
	id := r.PathValue("id")
	status := 200
	action := "user.updated"
	hash := ""
	if id == "" || b.Password != "" {
		var err error
		hash, err = hashPassword(b.Password)
		if err != nil {
			return err
		}
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(18501)"); err != nil {
		return err
	}
	if id == "" {
		id = newID()
		status = 201
		action = "user.created"
		_, err = tx.Exec(r.Context(), "INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,$4)", id, b.Email, hash, b.Role)
	} else {
		var oldRole string
		if err = tx.QueryRow(r.Context(), "SELECT role FROM users WHERE id=$1 FOR UPDATE", id).Scan(&oldRole); err != nil {
			return dbError(err)
		}
		if oldRole == "admin" && b.Role != "admin" {
			if err = preserveAdministrator(r.Context(), tx, id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(r.Context(), "UPDATE users SET email=$2,role=$3,password_hash=CASE WHEN $4='' THEN password_hash ELSE $4 END WHERE id=$1", id, b.Email, b.Role, hash)
		if err == nil {
			_, err = tx.Exec(r.Context(), "DELETE FROM auth_sessions WHERE user_id=$1", id)
		}
	}
	if err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, action, id, map[string]string{"email": b.Email, "role": b.Role}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, status, User{ID: id, Email: b.Email, Role: b.Role})
	return nil
}
func preserveAdministrator(ctx context.Context, tx pgx.Tx, id string) error {
	var other bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE role='admin' AND id<>$1)", id).Scan(&other); err != nil {
		return err
	}
	if !other {
		return fail(409, "LAST_ADMIN", "At least one administrator must remain")
	}
	return nil
}
func (a *App) deleteUser(w http.ResponseWriter, r *http.Request, u User) error {
	id := r.PathValue("id")
	if id == u.ID {
		return fail(409, "CURRENT_USER", "Cannot delete your own account")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(18501)"); err != nil {
		return err
	}
	var role string
	if err = tx.QueryRow(r.Context(), "SELECT role FROM users WHERE id=$1 FOR UPDATE", id).Scan(&role); err != nil {
		return dbError(err)
	}
	if role == "admin" {
		if err = preserveAdministrator(r.Context(), tx, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM auth_sessions WHERE user_id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM users WHERE id=$1", id); err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, "user.deleted", id, nil); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
