package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/jackc/pgx/v5"
	"github.com/ygandwx/BarePulse/backend/internal/drivers"
)

// PowerProfile is loaded from an administrator-owned secret file at startup.
// It is never returned by APIs or recorded in audit events.
type PowerProfile struct {
	DeviceID               string              `json:"device_id"`
	Kind                   string              `json:"kind"`
	Endpoint               string              `json:"endpoint"`
	Approved               bool                `json:"approved"`
	Evidence               string              `json:"evidence"`
	AuthorizationExpiresAt time.Time           `json:"authorization_expires_at"`
	AllowedServerIDs       []string            `json:"allowed_server_ids"`
	AllowedPorts           []int               `json:"allowed_ports"`
	AllowedActions         []string            `json:"allowed_actions"`
	Username               string              `json:"username"`
	Password               string              `json:"password"`
	MappingVerified        bool                `json:"mapping_verified"`
	RedfishSystemPath      string              `json:"redfish_system_path"`
	APC                    *drivers.APCProfile `json:"apc_profile"`
	SNMP                   *PowerSNMPProfile   `json:"snmp_v3"`
	IPMI                   *PowerIPMIProfile   `json:"ipmi_profile"`
}
type PowerIPMIProfile struct {
	BinaryPath    string `json:"binary_path"`
	BinarySHA256  string `json:"binary_sha256"`
	ClientVersion string `json:"client_version"`
	UDPPort       uint16 `json:"udp_port"`
	Privilege     string `json:"privilege"`
}
type PowerSNMPProfile struct {
	AuthProtocol    string `json:"auth_protocol"`
	PrivacyProtocol string `json:"privacy_protocol"`
	AuthPassword    string `json:"auth_password"`
	PrivacyPassword string `json:"privacy_password"`
	DiscoveryOID    string `json:"discovery_oid"`
}

func LoadPowerProfiles(path string) (map[string]PowerProfile, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open device profiles file")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 2<<20))
	d.DisallowUnknownFields()
	var profiles map[string]PowerProfile
	if err = d.Decode(&profiles); err != nil {
		return nil, errors.New("invalid device profiles JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("device profiles file must contain one JSON object")
	}
	for ref, p := range profiles {
		if !validText(ref, 200) || !validText(p.DeviceID, 200) || !validText(p.Endpoint, 2000) {
			return nil, errors.New("device profile reference, device_id and endpoint are required")
		}
	}
	return profiles, nil
}

func (a *App) powerProfile(target drivers.Target, serverID, action, reference string) (PowerProfile, error) {
	var zero PowerProfile
	if !a.Config.AllowRealDevices || !a.Config.AllowDeviceWrites {
		return zero, fail(409, "REAL_DEVICES_DISABLED", "Real-device reads and writes require explicit deployment authorization")
	}
	p, ok := a.Config.PowerProfiles[reference]
	if !ok || reference == "" || !p.Approved || !validText(p.Evidence, 2000) || !time.Now().Before(p.AuthorizationExpiresAt) {
		return zero, fail(409, "DEVICE_APPROVAL_REQUIRED", "A current, documented device authorization profile is required")
	}
	if p.DeviceID != target.DeviceID || p.Kind != target.Kind || p.Endpoint != target.Address || !slices.Contains(p.AllowedServerIDs, serverID) || !slices.Contains(p.AllowedPorts, target.Port) || !slices.Contains(p.AllowedActions, action) {
		return zero, fail(409, "DEVICE_APPROVAL_MISMATCH", "Target or action is outside the exact approved profile")
	}
	if !validText(p.Username, 200) {
		return zero, fail(409, "DEVICE_CREDENTIALS_REQUIRED", "Device credentials are unavailable")
	}
	switch p.Kind {
	case "ipmi":
		if target.Port != 1 || len(p.Username) > 16 || len(p.Password) < 1 || len(p.Password) > 20 || strings.ContainsAny(p.Username+p.Password, "\x00\r\n") {
			return zero, fail(409, "DEVICE_PROFILE_INVALID", "IPMI requires BMC port 1 and bounded server-owned credentials")
		}
		// Constructor validation is local and does not run the client or contact
		// a BMC. Executable integrity and version are checked before dispatch.
		if _, err := configuredIPMI(p); err != nil {
			return zero, err
		}
	case "pikvm", "redfish":
		u, err := url.Parse(p.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || p.Password == "" {
			return zero, fail(409, "DEVICE_PROFILE_INVALID", "HTTP device profile requires an exact HTTPS origin and credentials")
		}
		// Literal enrolled IPs avoid DNS rebinding from escaping the approval target.
		if net.ParseIP(u.Hostname()) == nil {
			return zero, fail(409, "DEVICE_PROFILE_INVALID", "Use a verified literal management IP in the approved HTTPS endpoint")
		}
		if p.Kind == "pikvm" && !p.MappingVerified {
			return zero, fail(409, "MAPPING_UNVERIFIED", "PiKVM port mappings must be verified before physical power actions")
		}
		if p.Kind == "redfish" && (!strings.HasPrefix(p.RedfishSystemPath, "/redfish/v1/Systems/") || strings.ContainsAny(strings.TrimPrefix(p.RedfishSystemPath, "/redfish/v1/Systems/"), "/%?#\\") || strings.TrimPrefix(p.RedfishSystemPath, "/redfish/v1/Systems/") == "") {
			return zero, fail(409, "SYSTEM_BINDING_REQUIRED", "Select an explicitly verified Redfish ComputerSystem path")
		}
	case "apc":
		host, port, err := net.SplitHostPort(p.Endpoint)
		if err != nil {
			host = p.Endpoint
			port = "161"
		}
		n, parseErr := strconv.ParseUint(port, 10, 16)
		if net.ParseIP(host) == nil || parseErr != nil || n == 0 || p.APC == nil || !p.APC.Confirmed || p.APC.Model == "" || p.APC.Firmware == "" || p.APC.Evidence == "" || p.SNMP == nil {
			return zero, fail(409, "DEVICE_PROFILE_INVALID", "APC requires an exact IP, confirmed firmware/outlet profile and SNMPv3 credentials")
		}
		outlet, exists := p.APC.Outlets[target.Port]
		if !exists || outlet.StateOID == "" || outlet.ControlOID == "" {
			return zero, fail(409, "CAPABILITY_UNVERIFIED", "APC outlet OIDs must be supplied from verified firmware evidence")
		}
		if _, exists = outlet.Actions[action]; !exists {
			return zero, fail(409, "CAPABILITY_UNSUPPORTED", "Approved APC profile does not support this action")
		}
		if strings.TrimPrefix(p.SNMP.DiscoveryOID, ".") != strings.TrimPrefix(outlet.StateOID, ".") {
			return zero, fail(409, "DEVICE_PROFILE_INVALID", "SNMP discovery must use the confirmed read-only outlet state OID")
		}
		auth, privacy, err := snmpPowerAlgorithms(p.SNMP)
		if err != nil {
			return zero, err
		}
		if err := drivers.ValidateSNMPv3Security(p.Username, p.SNMP.AuthPassword, p.SNMP.PrivacyPassword, auth, privacy); err != nil {
			return zero, fail(409, "DEVICE_PROFILE_INVALID", "Bounded SNMPv3 AuthPriv credentials are required")
		}
	default:
		return zero, fail(409, "CAPABILITY_UNSUPPORTED", "No verified physical driver is available for this device kind")
	}
	return p, nil
}
func configuredIPMI(p PowerProfile) (drivers.Driver, error) {
	if p.IPMI == nil {
		return nil, fail(409, "DEVICE_PROFILE_INVALID", "IPMI requires an approved client profile")
	}
	d, err := drivers.NewIPMI(drivers.IPMIConfig{
		AllowReal: true, AllowWrites: true, DeviceID: p.DeviceID, Address: p.Endpoint,
		Approved: p.Approved, Evidence: p.Evidence, AuthorizationExpiresAt: p.AuthorizationExpiresAt,
		AllowedActions: p.AllowedActions, BinaryPath: p.IPMI.BinaryPath, BinarySHA256: p.IPMI.BinarySHA256,
		ClientVersion: p.IPMI.ClientVersion, UDPPort: p.IPMI.UDPPort, Privilege: p.IPMI.Privilege, Timeout: 15 * time.Second,
	})
	if err != nil {
		return nil, fail(409, "DEVICE_PROFILE_INVALID", "IPMI requires an exact approved IP, client version, binary checksum and action scope")
	}
	return d, nil
}
func snmpPowerAlgorithms(p *PowerSNMPProfile) (gosnmp.SnmpV3AuthProtocol, gosnmp.SnmpV3PrivProtocol, error) {
	if p == nil {
		return 0, 0, fail(409, "DEVICE_PROFILE_INVALID", "SNMPv3 profile is required")
	}
	auth, privacy, err := drivers.ParseSNMPv3Algorithms(p.AuthProtocol, p.PrivacyProtocol)
	// Username is separately validated on the resolved profile and transport.
	if err != nil || drivers.ValidateSNMPv3Security("profile-validation", p.AuthPassword, p.PrivacyPassword, auth, privacy) != nil || p.DiscoveryOID == "" {
		return 0, 0, fail(409, "DEVICE_PROFILE_INVALID", "Explicit SNMPv3 AuthPriv algorithms, passphrases and discovery OID are required")
	}
	return auth, privacy, nil
}
func configuredPowerDriver(p PowerProfile) (drivers.Driver, error) {
	var policy drivers.NetworkPolicy
	if p.Kind == "pikvm" || p.Kind == "redfish" {
		u, err := url.Parse(p.Endpoint)
		if err != nil || u.Host == "" {
			return nil, fail(409, "DEVICE_PROFILE_INVALID", "Invalid HTTP device origin")
		}
		policy = drivers.NetworkPolicy{AllowReal: true, AllowWrite: true, AllowedHosts: []string{u.Host}}
	}
	switch p.Kind {
	case "ipmi":
		return configuredIPMI(p)
	case "pikvm":
		return drivers.NewPiKVM(drivers.PiKVMConfig{Network: policy, Timeout: 5 * time.Second, MappingVerified: p.MappingVerified}), nil
	case "redfish":
		return drivers.NewRedfish(drivers.RedfishConfig{Network: policy, Timeout: 5 * time.Second, SystemPath: p.RedfishSystemPath}), nil
	case "apc":
		auth, privacy, err := snmpPowerAlgorithms(p.SNMP)
		if err != nil {
			return nil, err
		}
		if err := drivers.ValidateSNMPv3Security(p.Username, p.SNMP.AuthPassword, p.SNMP.PrivacyPassword, auth, privacy); err != nil {
			return nil, fail(409, "DEVICE_PROFILE_INVALID", "Bounded SNMPv3 AuthPriv credentials are required")
		}
		transport := &drivers.SNMPV3Transport{AllowReal: true, AllowWrites: true, AllowedTargets: []string{p.Endpoint}, Username: p.Username, AuthPassword: p.SNMP.AuthPassword, PrivacyPassword: p.SNMP.PrivacyPassword, AuthProtocol: auth, PrivacyProtocol: privacy, DiscoveryOID: p.SNMP.DiscoveryOID, Timeout: 3 * time.Second}
		return &drivers.APC{Profile: *p.APC, Transport: transport, AllowReads: true, AllowWrites: true}, nil
	default:
		return nil, fail(409, "CAPABILITY_UNSUPPORTED", "No verified driver for this device kind")
	}
}

type OperationTask struct {
	ID             string          `json:"id"`
	ActorID        string          `json:"actor_id"`
	ServerID       string          `json:"server_id"`
	DeviceID       string          `json:"device_id"`
	BindingID      string          `json:"binding_id"`
	BindingVersion int64           `json:"binding_version"`
	ServerVersion  int64           `json:"server_version"`
	DeviceVersion  int64           `json:"device_version"`
	Action         string          `json:"action"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

const taskColumns = "t.id,t.actor_id,t.server_id,t.device_id,t.binding_id,t.binding_version,t.server_version,t.device_version,t.action,t.status,t.result,t.created_at,t.updated_at"

func scanTask(row pgx.Row) (OperationTask, error) {
	var t OperationTask
	err := row.Scan(&t.ID, &t.ActorID, &t.ServerID, &t.DeviceID, &t.BindingID, &t.BindingVersion, &t.ServerVersion, &t.DeviceVersion, &t.Action, &t.Status, &t.Result, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}
func (a *App) registerOperations() {
	a.route("POST /api/v1/servers/{id}/power-actions", false, a.submitPower)
	a.route("GET /api/v1/servers/{id}/power", false, a.powerSummary)
	a.route("GET /api/v1/tasks", false, a.listTasks)
	a.route("GET /api/v1/tasks/{id}", false, a.getTask)
	a.route("DELETE /api/v1/tasks/{id}", false, a.cancelTask)
	a.route("POST /api/v1/tasks/{id}/resolve", true, a.resolveTask)
	a.route("GET /api/v1/audit", false, a.listAudit)
}

func powerActions(kind, role string) []string {
	actions := []string{}
	switch kind {
	case "mock_pikvm", "pikvm":
		actions = []string{"power_on", "request_shutdown", "hard_reset"}
	case "mock_bmc", "redfish", "ipmi":
		actions = []string{"power_on", "request_shutdown", "hard_reset", "power_cycle"}
	case "mock_pdu", "apc":
		actions = []string{"power_on", "power_cycle"}
	}
	if role == "admin" && (kind == "pikvm" || kind == "mock_pikvm") {
		actions = append(actions, "enable_port", "disable_port")
	}
	if role == "operator" {
		return []string{}
	}
	return actions
}
func permitsPower(kind, role, action string) bool {
	for _, v := range powerActions(kind, role) {
		if v == action {
			return true
		}
	}
	return false
}

func (a *App) submitPower(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "operator" {
		return fail(403, "FORBIDDEN", "Operators have read-only access")
	}
	var b struct {
		Action string `json:"action"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(key) {
		return fail(400, "IDEMPOTENCY_REQUIRED", "Idempotency-Key must contain 8–128 printable characters")
	}
	id := r.PathValue("id")
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	s, err := scanServer(tx.QueryRow(r.Context(), "SELECT "+serverColumns+" FROM servers WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return dbError(err)
	}
	if u.Role != "admin" && s.OwnerID != u.ID {
		return fail(404, "NOT_FOUND", "Server not found")
	}
	// The server row serializes concurrent requests before the unique key check.
	previous, e := scanTask(tx.QueryRow(r.Context(), "SELECT "+taskColumns+" FROM operation_tasks t WHERE actor_id=$1 AND server_id=$2 AND idempotency_key=$3", u.ID, id, key))
	if e == nil {
		if previous.Action != b.Action {
			return fail(409, "IDEMPOTENCY_CONFLICT", "This key already identifies a different action")
		}
		jsonResponse(w, 200, previous)
		return nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if s.Maintenance {
		return fail(409, "MAINTENANCE", "Server is in maintenance mode")
	}
	if inactive, e := powerServiceInactive(r.Context(), tx, s.ID); e != nil {
		return e
	} else if inactive {
		return fail(409, "SERVICE_INACTIVE", "Server service is suspended or terminated")
	}
	var binding Binding
	binding, err = scanBinding(tx.QueryRow(r.Context(), "SELECT "+bindingColumns+" FROM bindings b WHERE b.server_id=$1 AND b.kind='power' AND b.active", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(409, "NO_POWER_BINDING", "Configure an active power binding first")
	}
	if err != nil {
		return err
	}
	var kind, address, reference string
	var enabled bool
	var blocked string
	var lease *string
	var version int64
	var leaseKind *string
	if err = tx.QueryRow(r.Context(), "SELECT kind,address,credential_ref,enabled,blocked_reason,lease_owner,version,lease_kind FROM devices WHERE id=$1 FOR UPDATE", binding.DeviceID).Scan(&kind, &address, &reference, &enabled, &blocked, &lease, &version, &leaseKind); err != nil {
		return err
	}
	if !permitsPower(kind, u.Role, b.Action) {
		return fail(400, "CAPABILITY_UNSUPPORTED", "Action is unavailable for this device or role")
	}
	if !strings.HasPrefix(kind, "mock_") {
		if _, e := a.powerProfile(drivers.Target{DeviceID: binding.DeviceID, Kind: kind, Address: address, Port: binding.Port}, s.ID, b.Action, reference); e != nil {
			return e
		}
	}
	if !enabled {
		return fail(409, "DEVICE_DISABLED", "Device is disabled")
	}
	if blocked != "" {
		return fail(409, "DEVICE_BLOCKED", "Device needs an administrator to resolve an earlier unknown result")
	}
	if lease != nil && leaseKind != nil && *leaseKind == "console" {
		return fail(409, "PROTECTED_SESSION", "Release the device console safely before submitting power actions")
	}
	taskID := newID()
	_, err = tx.Exec(r.Context(), `INSERT INTO operation_tasks(id,actor_id,server_id,device_id,binding_id,binding_version,server_version,device_version,action,idempotency_key,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'queued')`, taskID, u.ID, id, binding.DeviceID, binding.ID, binding.Version, s.Version, version, b.Action, key)
	if err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, "power.queued", taskID, map[string]any{"server_id": id, "device_id": binding.DeviceID, "action": b.Action, "binding_id": binding.ID, "binding_version": binding.Version}); err != nil {
		return err
	}
	task, err := scanTask(tx.QueryRow(r.Context(), "SELECT "+taskColumns+" FROM operation_tasks t WHERE t.id=$1", taskID))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 202, task)
	return nil
}
func validIdempotencyKey(key string) bool {
	if len(key) < 8 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

type powerQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func powerServiceInactive(ctx context.Context, tx powerQueryer, serverID string) (bool, error) {
	var inactive bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_mappings WHERE server_id=$1 AND state<>'active') AND NOT EXISTS(SELECT 1 FROM service_mappings WHERE server_id=$1 AND state='active')`, serverID).Scan(&inactive)
	return inactive, err
}

func (a *App) listTasks(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), "SELECT "+taskColumns+" FROM operation_tasks t JOIN servers s ON s.id=t.server_id WHERE ($1='admin' OR (s.owner_id=$2 AND t.actor_id=$2)) AND ($3='' OR t.server_id=$3) ORDER BY t.created_at DESC LIMIT 200", u.Role, u.ID, r.URL.Query().Get("server_id"))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []OperationTask{}
	for rows.Next() {
		task, e := scanTask(rows)
		if e != nil {
			return e
		}
		items = append(items, task)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, items)
	return nil
}
func (a *App) visibleTask(ctx context.Context, id string, u User) (OperationTask, error) {
	task, err := scanTask(a.DB.QueryRow(ctx, "SELECT "+taskColumns+" FROM operation_tasks t JOIN servers s ON s.id=t.server_id WHERE t.id=$1 AND ($2='admin' OR (s.owner_id=$3 AND t.actor_id=$3))", id, u.Role, u.ID))
	if err != nil {
		return task, dbError(err)
	}
	return task, nil
}
func (a *App) getTask(w http.ResponseWriter, r *http.Request, u User) error {
	task, err := a.visibleTask(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	jsonResponse(w, 200, task)
	return nil
}
func (a *App) powerSummary(w http.ResponseWriter, r *http.Request, u User) error {
	s, err := a.server(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	var kind, deviceID, address, reference string
	var port int
	var enabled bool
	var cooldown *time.Time
	var blocked string
	err = a.DB.QueryRow(r.Context(), "SELECT d.kind,d.id,d.address,d.credential_ref,b.port,d.enabled,d.cooldown_until,d.blocked_reason FROM bindings b JOIN devices d ON d.id=b.device_id WHERE b.server_id=$1 AND b.kind='power' AND b.active", s.ID).Scan(&kind, &deviceID, &address, &reference, &port, &enabled, &cooldown, &blocked)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonResponse(w, 200, map[string]any{"quality": "unbound", "power_state": "unknown", "supply_state": "unknown", "actions": []string{}})
		return nil
	}
	if err != nil {
		return err
	}
	task, e := scanTask(a.DB.QueryRow(r.Context(), "SELECT "+taskColumns+" FROM operation_tasks t JOIN bindings b ON b.id=t.binding_id JOIN devices d ON d.id=t.device_id WHERE t.server_id=$1 AND b.active AND t.binding_version=b.version AND t.server_version=$2 AND t.device_version=d.version AND t.status IN ('succeeded','failed','unknown') ORDER BY t.updated_at DESC LIMIT 1", s.ID, s.Version))
	actions := powerActions(kind, u.Role)
	if !strings.HasPrefix(kind, "mock_") {
		approved := []string{}
		for _, action := range actions {
			if _, err := a.powerProfile(drivers.Target{DeviceID: deviceID, Kind: kind, Address: address, Port: port}, s.ID, action, reference); err == nil {
				approved = append(approved, action)
			}
		}
		actions = approved
	}
	inactive, err := powerServiceInactive(r.Context(), a.DB, s.ID)
	if err != nil {
		return err
	}
	result := map[string]any{"quality": "not_observed", "power_state": "unknown", "supply_state": "unknown", "actions": actions, "simulated": strings.HasPrefix(kind, "mock_"), "execution_enabled": enabled && len(actions) > 0 && blocked == "" && !inactive && !s.Maintenance, "blocked_reason": blocked, "cooldown_until": cooldown, "service_inactive": inactive}
	if e == nil {
		result["last_task"] = task
		result["observed_at"] = task.UpdatedAt
		var outcome drivers.Result
		if json.Unmarshal(task.Result, &outcome) == nil {
			if outcome.PowerState != "" {
				result["power_state"] = outcome.PowerState
			}
			if outcome.SupplyState != "" {
				result["supply_state"] = outcome.SupplyState
			}
		}
		result["quality"] = "cached"
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	jsonResponse(w, 200, result)
	return nil
}
func (a *App) cancelTask(w http.ResponseWriter, r *http.Request, u User) error {
	if u.Role == "operator" {
		return fail(403, "FORBIDDEN", "Read-only role cannot cancel tasks")
	}
	task, err := a.visibleTask(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), "UPDATE operation_tasks SET status='cancelled',result=$2,updated_at=now() WHERE id=$1 AND status='queued'", task.ID, `{"code":"CANCELLED","message":"Cancelled before execution"}`)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fail(409, "TASK_ALREADY_STARTED", "Only queued tasks can be cancelled")
	}
	if err = audit(r.Context(), tx, u, "power.cancelled", task.ID, nil); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (a *App) resolveTask(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Outcome         string `json:"outcome"`
		Reason          string `json:"reason"`
		ExecutorStopped bool   `json:"executor_stopped"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	if (b.Outcome != "succeeded" && b.Outcome != "failed") || !validText(b.Reason, 2000) || !b.ExecutorStopped {
		return fail(400, "RESOLUTION_REQUIRED", "Record the verified outcome, evidence/reason and confirmation that the old executor is stopped")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	task, err := scanTask(tx.QueryRow(r.Context(), "SELECT "+taskColumns+" FROM operation_tasks t WHERE t.id=$1 FOR UPDATE", r.PathValue("id")))
	if err != nil {
		return dbError(err)
	}
	if task.Status != "unknown" {
		return fail(409, "NOT_UNKNOWN", "Only an unknown outcome can be manually reconciled")
	}
	var kind string
	var lease *string
	if err = tx.QueryRow(r.Context(), "SELECT kind,lease_owner FROM devices WHERE id=$1 FOR UPDATE", task.DeviceID).Scan(&kind, &lease); err != nil {
		return err
	}
	if !strings.HasPrefix(kind, "mock_") {
		return fail(409, "REAL_DEVICES_DISABLED", "Physical-device reconciliation requires an authorized hardware workflow")
	}
	if lease == nil || *lease != task.ID {
		return fail(409, "LEASE_CHANGED", "Device lease no longer matches this task")
	}
	result := map[string]any{"status": b.Outcome, "code": "MANUALLY_RECONCILED", "message": b.Reason, "accepted": false, "completed": false, "target_reached": false, "simulated": true, "manual_verification": true, "previous_result": json.RawMessage(task.Result)}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "UPDATE operation_tasks SET status=$2,result=$3,updated_at=now() WHERE id=$1", task.ID, b.Outcome, encoded); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "UPDATE devices SET lease_owner=NULL,lease_kind=NULL,blocked_reason='',updated_at=now() WHERE id=$1 AND lease_owner=$2", task.DeviceID, task.ID); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "power.reconciled", task.ID, map[string]any{"outcome": b.Outcome, "reason": b.Reason, "executor_stopped": b.ExecutorStopped}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 200, result)
	return nil
}
func (a *App) listAudit(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), "SELECT id,actor_id,action,object_id,details,created_at FROM audit_events WHERE $1='admin' OR actor_id=$2 ORDER BY created_at DESC LIMIT 300", u.Role, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type event struct {
		ID        string          `json:"id"`
		ActorID   *string         `json:"actor_id"`
		Action    string          `json:"action"`
		ObjectID  string          `json:"object_id"`
		Details   json.RawMessage `json:"details"`
		CreatedAt time.Time       `json:"created_at"`
	}
	items := []event{}
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.ObjectID, &e.Details, &e.CreatedAt); err != nil {
			return err
		}
		items = append(items, e)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, items)
	return nil
}

// A session advisory lock elects one worker. Persisted leases are never reassigned merely
// because this lock disappeared: commands from the previous executor may still be running.
func (a *App) RunWorker(ctx context.Context) {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		slog.Error("power worker connection", "error", err)
		return
	}
	defer conn.Release()
	var leader bool
	if err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(18502)").Scan(&leader); err != nil || !leader {
		return
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(18502)")
	if err = a.recoverPowerTasks(ctx); err != nil {
		slog.Error("power recovery failed", "error", err)
		return
	}
	driver := drivers.NewMock()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = conn.Ping(ctx); err != nil {
			return
		}
		worked, e := a.runOnePowerTask(ctx, driver)
		if e != nil {
			slog.Error("power worker task", "error", e)
		}
		if worked && e == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) recoverPowerTasks(ctx context.Context) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "UPDATE operation_tasks SET status='unknown',result=$1::jsonb || jsonb_build_object('simulated',(SELECT kind LIKE 'mock_%' FROM devices WHERE id=operation_tasks.device_id)),updated_at=now() WHERE status IN ('running','verifying') RETURNING id,actor_id,device_id", `{"status":"unknown","code":"EXECUTOR_LOST","message":"Previous executor ended without a verified result; command was not replayed","accepted":false,"completed":false,"target_reached":false}`)
	if err != nil {
		return err
	}
	type lost struct{ id, actor, device string }
	items := []lost{}
	for rows.Next() {
		var item lost
		if err = rows.Scan(&item.id, &item.actor, &item.device); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		if _, err = tx.Exec(ctx, "UPDATE devices SET blocked_reason='EXECUTOR_LOST',updated_at=now() WHERE id=$1 AND lease_owner=$2", item.device, item.id); err != nil {
			return err
		}
		if err = audit(ctx, tx, User{ID: item.actor}, "power.executor_lost", item.id, map[string]string{"device_id": item.device}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *App) runOnePowerTask(ctx context.Context, driver drivers.Driver) (bool, error) {
	return a.runPowerTaskWithFactory(ctx, driver, configuredPowerDriver)
}

// The factory parameter is an internal test seam, never a request/configuration field.
func (a *App) runPowerTaskWithFactory(ctx context.Context, driver drivers.Driver, factory func(PowerProfile) (drivers.Driver, error)) (bool, error) {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	task, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM operation_tasks t JOIN devices d ON d.id=t.device_id WHERE t.status='queued' AND d.lease_owner IS NULL AND d.blocked_reason='' AND (d.cooldown_until IS NULL OR d.cooldown_until<=now()) AND NOT EXISTS(SELECT 1 FROM operation_tasks o WHERE o.server_id=t.server_id AND o.status IN ('running','verifying','unknown')) ORDER BY t.created_at,t.id FOR UPDATE OF t SKIP LOCKED LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s, err := scanServer(tx.QueryRow(ctx, "SELECT "+serverColumns+" FROM servers WHERE id=$1 FOR UPDATE", task.ServerID))
	if err != nil {
		return false, err
	}
	var target drivers.Target
	var reference string
	var enabled bool
	var version int64
	var lease *string
	var blocked string
	var cooldown *time.Time
	target.DeviceID = task.DeviceID
	if err = tx.QueryRow(ctx, "SELECT kind,address,credential_ref,scenario,enabled,version,lease_owner,blocked_reason,cooldown_until FROM devices WHERE id=$1 FOR UPDATE", task.DeviceID).Scan(&target.Kind, &target.Address, &reference, &target.Scenario, &enabled, &version, &lease, &blocked, &cooldown); err != nil {
		return false, err
	}
	if lease != nil || blocked != "" || (cooldown != nil && cooldown.After(time.Now())) {
		return false, nil
	}
	binding, err := scanBinding(tx.QueryRow(ctx, "SELECT "+bindingColumns+" FROM bindings b WHERE b.id=$1", task.BindingID))
	if err != nil {
		return false, err
	}
	target.Port = binding.Port
	var actor User
	if err = tx.QueryRow(ctx, "SELECT id,email,role FROM users WHERE id=$1", task.ActorID).Scan(&actor.ID, &actor.Email, &actor.Role); err != nil {
		return false, err
	}
	code := ""
	message := ""
	inactive, err := powerServiceInactive(ctx, tx, s.ID)
	if err != nil {
		return false, err
	}
	switch {
	case !binding.Active || binding.Version != task.BindingVersion || binding.DeviceID != task.DeviceID || binding.ServerID != task.ServerID || s.Version != task.ServerVersion || version != task.DeviceVersion:
		code = "BINDING_CHANGED"
		message = "Server, device or binding changed after task submission"
	case actor.Role != "admin" && (actor.Role != "customer" || s.OwnerID != actor.ID):
		code = "FORBIDDEN"
		message = "Actor no longer owns the server or has execution permission"
	case s.Maintenance:
		code = "MAINTENANCE"
		message = "Server entered maintenance"
	case inactive:
		code = "SERVICE_INACTIVE"
		message = "Server service is suspended or terminated"
	case !enabled:
		code = "DEVICE_DISABLED"
		message = "Device is disabled"
	case !permitsPower(target.Kind, actor.Role, task.Action):
		code = "CAPABILITY_UNSUPPORTED"
		message = "Action is not supported by current device capability"
	}
	var approvedUntil time.Time
	if code == "" && !strings.HasPrefix(target.Kind, "mock_") {
		profile, e := a.powerProfile(target, s.ID, task.Action, reference)
		if e == nil {
			driver, e = factory(profile)
			target.Username = profile.Username
			target.Password = profile.Password
			approvedUntil = profile.AuthorizationExpiresAt
		}
		if e != nil {
			var ae *apiError
			if errors.As(e, &ae) {
				code = ae.Code
				message = ae.Message
			} else {
				code = "DEVICE_PROFILE_INVALID"
				message = "Configured device driver could not be initialized"
			}
		}
	}
	if code != "" {
		result, _ := json.Marshal(drivers.Result{Status: "failed", Code: code, Message: message})
		if _, err = tx.Exec(ctx, "UPDATE operation_tasks SET status='failed',result=$2,updated_at=now() WHERE id=$1", task.ID, result); err != nil {
			return false, err
		}
		if err = audit(ctx, tx, actor, "power.rejected_before_execution", task.ID, map[string]string{"code": code}); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "UPDATE devices SET lease_owner=$2,lease_kind='power',updated_at=now() WHERE id=$1", task.DeviceID, task.ID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE operation_tasks SET status='running',updated_at=now() WHERE id=$1", task.ID); err != nil {
		return false, err
	}
	if err = audit(ctx, tx, actor, "power.started", task.ID, map[string]any{"server_id": task.ServerID, "device_id": task.DeviceID, "port": target.Port, "simulated": strings.HasPrefix(target.Kind, "mock_")}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	execCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result := drivers.Result{Status: "failed", Code: "DEVICE_APPROVAL_EXPIRED", Message: "Authorization window ended before dispatch"}
	if approvedUntil.IsZero() || time.Now().Before(approvedUntil) {
		result = executePowerSafely(execCtx, driver, target, task.Action)
	}
	// Persist uncertainty even if shutdown cancelled the request context.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return true, a.finishPowerTask(finishCtx, task, actor, result, strings.HasPrefix(target.Kind, "mock_"))
}
func executePowerSafely(ctx context.Context, driver drivers.Driver, target drivers.Target, action string) (result drivers.Result) {
	defer func() {
		if recover() != nil {
			result = drivers.Result{Status: "unknown", Code: "EXECUTOR_PANIC", Message: "Executor failed after dispatch; result is unknown"}
		}
	}()
	result = driver.Execute(ctx, target, action)
	if result.Status != "succeeded" && result.Status != "failed" && result.Status != "unknown" {
		result.Status = "unknown"
		result.Code = "INVALID_DRIVER_RESULT"
	}
	if result.Status == "succeeded" && !result.Completed {
		result.Status = "unknown"
		if result.Code == "" {
			result.Code = "ACCEPTED_UNVERIFIED"
		}
	}
	return result
}
func (a *App) finishPowerTask(ctx context.Context, task OperationTask, actor User, result drivers.Result, simulated bool) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var lease *string
	if err = tx.QueryRow(ctx, "SELECT lease_owner FROM devices WHERE id=$1 FOR UPDATE", task.DeviceID).Scan(&lease); err != nil {
		return err
	}
	if lease == nil || *lease != task.ID {
		return fail(409, "LEASE_CHANGED", "Executor lost its device lease; refusing to overwrite newer state")
	}
	encoded, err := json.Marshal(struct {
		drivers.Result
		Simulated bool `json:"simulated"`
	}{result, simulated})
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, "UPDATE operation_tasks SET status=$2,result=$3,updated_at=now() WHERE id=$1 AND status='running'", task.ID, result.Status, encoded)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fail(409, "TASK_CHANGED", "Task status changed; preserving device lease")
	}
	cooldown := result.CooldownSeconds
	if (task.Action == "enable_port" || task.Action == "disable_port") && cooldown < 30 {
		cooldown = 30
	}
	if result.Status == "unknown" {
		_, err = tx.Exec(ctx, "UPDATE devices SET blocked_reason='OUTCOME_UNKNOWN',cooldown_until=CASE WHEN $2>0 THEN now()+($2 * interval '1 second') ELSE cooldown_until END,updated_at=now() WHERE id=$1", task.DeviceID, cooldown)
	} else {
		_, err = tx.Exec(ctx, "UPDATE devices SET lease_owner=NULL,lease_kind=NULL,cooldown_until=CASE WHEN $2>0 THEN now()+($2 * interval '1 second') ELSE cooldown_until END,updated_at=now() WHERE id=$1", task.DeviceID, cooldown)
	}
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, "power."+result.Status, task.ID, map[string]any{"code": result.Code, "accepted": result.Accepted, "completed": result.Completed, "target_reached": result.TargetReached, "simulated": simulated}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
