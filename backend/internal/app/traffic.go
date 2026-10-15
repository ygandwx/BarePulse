package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
)

type TrafficAccount struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	OwnerID            string `json:"owner_id"`
	QuotaBytes         string `json:"quota_bytes"`
	Direction          string `json:"direction"`
	CycleDay           int    `json:"cycle_day"`
	Timezone           string `json:"timezone"`
	AlertPercent       int    `json:"alert_percent"`
	EnforcementEnabled bool   `json:"enforcement_enabled"`
}

type TrafficSource struct {
	ID              string   `json:"id"`
	AccountID       string   `json:"account_id"`
	DeviceID        string   `json:"device_id"`
	BindingID       string   `json:"binding_id"`
	BindingVersion  int64    `json:"binding_version"`
	Kind            string   `json:"kind"`
	Role            string   `json:"role"`
	NetworkDomain   string   `json:"network_domain"`
	VLANID          int      `json:"vlan_id"`
	IfName          string   `json:"if_name"`
	ExpectedIfIndex int      `json:"expected_if_index"`
	Direction       string   `json:"direction"`
	IntervalSeconds int      `json:"interval_seconds"`
	MaxBPS          string   `json:"max_bps"`
	Mock            bool     `json:"mock"`
	AutoCollect     bool     `json:"auto_collect"`
	Active          bool     `json:"active"`
	VLANMemberIDs   []string `json:"vlan_member_ids,omitempty"`
}

type TrafficSample struct {
	ID             string    `json:"id,omitempty"`
	SampleKey      string    `json:"sample_key"`
	ObservedAt     time.Time `json:"observed_at"`
	InOctets       string    `json:"in_octets"`
	OutOctets      string    `json:"out_octets"`
	IfIndex        int       `json:"if_index"`
	IfName         string    `json:"if_name"`
	IfAlias        string    `json:"if_alias"`
	Uptime         string    `json:"uptime"`
	Discontinuity  string    `json:"discontinuity"`
	LinkState      string    `json:"link_state"`
	BindingVersion int64     `json:"binding_version"`
	Quality        string    `json:"quality,omitempty"`
	ErrorCode      string    `json:"error_code"`
}

type TrafficInterval struct {
	StartsAt      time.Time  `json:"starts_at"`
	EndsAt        time.Time  `json:"ends_at"`
	PeriodStart   *time.Time `json:"period_start"`
	UploadBytes   *string    `json:"upload_bytes"`
	DownloadBytes *string    `json:"download_bytes"`
	Quality       string     `json:"quality"`
	Billable      bool       `json:"billable"`
	SourceID      string     `json:"source_id,omitempty"`
}

var trafficDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,29})$`)
var trafficSignedDecimal = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,28})$`)
var trafficDomain = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,99}$`)

func trafficNumber(s string, counter bool) (*big.Int, error) {
	if !trafficDecimal.MatchString(s) {
		return nil, fail(400, "invalid_bytes", "Bytes and counters must be decimal strings")
	}
	n, _ := new(big.Int).SetString(s, 10)
	if counter && n.BitLen() > 64 {
		return nil, fail(400, "invalid_counter", "Counter exceeds unsigned 64-bit range")
	}
	return n, nil
}

func trafficPeriod(t time.Time, ac TrafficAccount) time.Time {
	loc, err := time.LoadLocation(ac.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := t.In(loc)
	start := trafficBoundary(local.Year(), local.Month(), ac.CycleDay, loc)
	if local.Before(start) {
		previous := time.Date(local.Year(), local.Month()-1, 1, 0, 0, 0, 0, loc)
		start = trafficBoundary(previous.Year(), previous.Month(), ac.CycleDay, loc)
	}
	return start.UTC()
}

func trafficBoundary(year int, month time.Month, day int, loc *time.Location) time.Time {
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, loc)
}
func trafficNextPeriod(period time.Time, ac TrafficAccount) time.Time {
	loc := mustTrafficLocation(ac.Timezone)
	local := period.In(loc)
	next := time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, loc)
	return trafficBoundary(next.Year(), next.Month(), ac.CycleDay, loc).UTC()
}

// evaluateTraffic never guesses a reset, wrap, remap, or cycle split. Unknown intervals
// retain NULL byte amounts; cross-cycle amounts stay unallocated until reviewed.
func evaluateTraffic(prev, cur TrafficSample, src TrafficSource, ac TrafficAccount, missed bool) TrafficInterval {
	result := TrafficInterval{StartsAt: prev.ObservedAt, EndsAt: cur.ObservedAt, Quality: "normal", SourceID: src.ID}
	if cur.BindingVersion != prev.BindingVersion || cur.BindingVersion != src.BindingVersion {
		result.Quality = "binding_changed"
		return result
	}
	if prev.IfIndex != cur.IfIndex || cur.IfIndex != src.ExpectedIfIndex || prev.IfName != cur.IfName || cur.IfName != src.IfName {
		result.Quality = "interface_remapped"
		return result
	}
	up1, _ := new(big.Int).SetString(prev.Uptime, 10)
	up2, _ := new(big.Int).SetString(cur.Uptime, 10)
	if up1 == nil || up2 == nil || up2.Cmp(up1) < 0 {
		result.Quality = "uptime_reset"
		return result
	}
	if prev.Discontinuity != cur.Discontinuity {
		result.Quality = "counter_discontinuity"
		return result
	}
	in1, _ := new(big.Int).SetString(prev.InOctets, 10)
	in2, _ := new(big.Int).SetString(cur.InOctets, 10)
	out1, _ := new(big.Int).SetString(prev.OutOctets, 10)
	out2, _ := new(big.Int).SetString(cur.OutOctets, 10)
	if in1 == nil || in2 == nil || out1 == nil || out2 == nil {
		result.Quality = "missing_counter"
		return result
	}
	inDelta := new(big.Int).Sub(in2, in1)
	outDelta := new(big.Int).Sub(out2, out1)
	if inDelta.Sign() < 0 || outDelta.Sign() < 0 {
		result.Quality = "counter_reset"
		return result
	}
	elapsed := cur.ObservedAt.Sub(prev.ObservedAt)
	if elapsed <= 0 {
		result.Quality = "out_of_order"
		return result
	}
	max, _ := new(big.Int).SetString(src.MaxBPS, 10)
	if max != nil && max.Sign() > 0 {
		limit := new(big.Int).Mul(max, big.NewInt(elapsed.Nanoseconds()))
		for _, delta := range []*big.Int{inDelta, outDelta} {
			bits := new(big.Int).Mul(delta, big.NewInt(8*int64(time.Second)))
			if bits.Cmp(limit) > 0 {
				result.Quality = "implausible_jump"
				return result
			}
		}
	}
	upload, download := inDelta.String(), outDelta.String()
	if src.Direction == "rx_download" {
		upload, download = download, upload
	}
	result.UploadBytes = &upload
	result.DownloadBytes = &download
	period := trafficPeriod(prev.ObservedAt, ac)
	if !period.Equal(trafficPeriod(cur.ObservedAt.Add(-time.Nanosecond), ac)) {
		result.Quality = "boundary_unallocated"
		return result
	}
	result.PeriodStart = &period
	if missed || elapsed > time.Duration(src.IntervalSeconds)*time.Second*3/2 {
		result.Quality = "recovered_gap"
	}
	result.Billable = src.Role == "primary"
	return result
}

func (a *App) registerTraffic() {
	a.route("GET /api/v1/traffic/accounts", false, a.listTrafficAccounts)
	a.route("POST /api/v1/traffic/accounts", true, a.createTrafficAccount)
	a.route("GET /api/v1/traffic/accounts/{id}", false, a.getTrafficAccount)
	a.route("GET /api/v1/servers/{id}/traffic", false, a.getServerTraffic)
	a.route("DELETE /api/v1/traffic/accounts/{id}", true, a.retireTrafficAccount)
	a.route("POST /api/v1/traffic/accounts/{id}/members", true, a.addTrafficMember)
	a.route("DELETE /api/v1/traffic/accounts/{id}/members/{server_id}", true, a.removeTrafficMember)
	a.route("POST /api/v1/traffic/accounts/{id}/members/{server_id}/refresh", true, a.refreshTrafficMember)
	a.route("POST /api/v1/traffic/accounts/{id}/adjustments", true, a.adjustTrafficAccount)
	a.route("POST /api/v1/traffic/sources", true, a.createTrafficSource)
	a.route("DELETE /api/v1/traffic/sources/{id}", true, a.retireTrafficSource)
	a.route("POST /api/v1/traffic/sources/{id}/samples", true, a.ingestTrafficSample)
	a.route("POST /api/v1/traffic/sources/{id}/collect", true, a.collectTrafficSource)
}

func trafficScanAccount(row pgx.Row) (TrafficAccount, error) {
	var ac TrafficAccount
	err := row.Scan(&ac.ID, &ac.Name, &ac.OwnerID, &ac.QuotaBytes, &ac.Direction, &ac.CycleDay, &ac.Timezone, &ac.AlertPercent, &ac.EnforcementEnabled)
	return ac, err
}

const trafficAccountCols = `id,name,owner_id,quota_bytes::text,direction,cycle_day,timezone,alert_percent,enforcement_enabled`
const trafficSourceCols = `id,account_id,device_id,COALESCE(binding_id,''),binding_version,kind,role,network_domain,vlan_id,if_name,expected_if_index,direction,interval_seconds,max_bps::text,mock,auto_collect,active`

// Acquire before source/account/server row locks. Collection shares this lock;
// topology changes exclude collection while coverage and membership are changed.
func trafficTopologyLock(ctx context.Context, tx pgx.Tx, write bool) error {
	query := `SELECT pg_advisory_xact_lock_shared(834920114)`
	if write {
		query = `SELECT pg_advisory_xact_lock(834920114)`
	}
	_, err := tx.Exec(ctx, query)
	return err
}

func trafficScanSource(row pgx.Row) (TrafficSource, error) {
	var s TrafficSource
	err := row.Scan(&s.ID, &s.AccountID, &s.DeviceID, &s.BindingID, &s.BindingVersion, &s.Kind, &s.Role, &s.NetworkDomain, &s.VLANID, &s.IfName, &s.ExpectedIfIndex, &s.Direction, &s.IntervalSeconds, &s.MaxBPS, &s.Mock, &s.AutoCollect, &s.Active)
	return s, err
}

func (a *App) trafficAccount(ctx context.Context, id string, u User) (TrafficAccount, error) {
	ac, err := trafficScanAccount(a.DB.QueryRow(ctx, `SELECT `+trafficAccountCols+` FROM traffic_accounts WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ac, fail(404, "not_found", "Traffic account not found")
	}
	if err != nil {
		return ac, err
	}
	if u.Role != "admin" && u.Role != "operator" && ac.OwnerID != u.ID {
		return ac, fail(404, "not_found", "Traffic account not found")
	}
	return ac, nil
}

func (a *App) listTrafficAccounts(w http.ResponseWriter, r *http.Request, u User) error {
	rows, err := a.DB.Query(r.Context(), `SELECT `+trafficAccountCols+` FROM traffic_accounts WHERE $1 OR owner_id=$2 ORDER BY name`, u.Role == "admin" || u.Role == "operator", u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []TrafficAccount{}
	for rows.Next() {
		ac, e := trafficScanAccount(rows)
		if e != nil {
			return e
		}
		list = append(list, ac)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	jsonResponse(w, 200, list)
	return nil
}

func (a *App) createTrafficAccount(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Name               string   `json:"name"`
		OwnerID            string   `json:"owner_id"`
		QuotaBytes         string   `json:"quota_bytes"`
		Direction          string   `json:"direction"`
		CycleDay           int      `json:"cycle_day"`
		Timezone           string   `json:"timezone"`
		AlertPercent       int      `json:"alert_percent"`
		ServerIDs          []string `json:"server_ids"`
		EnforcementEnabled bool     `json:"enforcement_enabled"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	if b.EnforcementEnabled {
		return fail(400, "policy_disabled", "Automatic charging, limiting, disconnection and power actions are not enabled")
	}
	if strings.TrimSpace(b.Name) == "" || len(b.Name) > 120 || len(b.ServerIDs) == 0 || len(b.ServerIDs) > 100 {
		return fail(400, "invalid_account", "Name and 1–100 server members are required")
	}
	if _, err := trafficNumber(b.QuotaBytes, false); err != nil {
		return err
	}
	if b.Direction != "upload" && b.Direction != "download" && b.Direction != "both" {
		return fail(400, "invalid_direction", "Select upload, download or both")
	}
	if b.CycleDay == 0 {
		b.CycleDay = 1
	}
	if b.Timezone == "" {
		b.Timezone = "UTC"
	}
	if b.CycleDay < 1 || b.CycleDay > 31 || b.AlertPercent < 0 || b.AlertPercent > 100 {
		return fail(400, "invalid_policy", "Invalid cycle day or alert threshold")
	}
	if _, err := time.LoadLocation(b.Timezone); err != nil {
		return fail(400, "invalid_timezone", "Unknown IANA timezone")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	id := newID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO traffic_accounts(id,name,owner_id,quota_bytes,direction,cycle_day,timezone,alert_percent) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, b.Name, b.OwnerID, b.QuotaBytes, b.Direction, b.CycleDay, b.Timezone, b.AlertPercent); err != nil {
		return dbError(err)
	}
	seen := map[string]bool{}
	for _, sid := range b.ServerIDs {
		if seen[sid] {
			return fail(400, "duplicate_member", "Server appears more than once")
		}
		seen[sid] = true
		var owner string
		var version int64
		if err = tx.QueryRow(r.Context(), `SELECT owner_id,version FROM servers WHERE id=$1 FOR UPDATE`, sid).Scan(&owner, &version); err != nil {
			return dbError(err)
		}
		if owner != b.OwnerID {
			return fail(409, "ownership_mismatch", "All shared account members must have the same owner")
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO traffic_members(account_id,server_id,server_version) VALUES($1,$2,$3)`, id, sid, version); err != nil {
			return dbError(err)
		}
	}
	if err = audit(r.Context(), tx, u, "traffic.account.create", id, map[string]any{"server_ids": b.ServerIDs, "quota_bytes": b.QuotaBytes}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	ac, err := a.trafficAccount(r.Context(), id, u)
	if err != nil {
		return err
	}
	jsonResponse(w, 201, ac)
	return nil
}

func (a *App) createTrafficSource(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		TrafficSource
		CoverageConfirmed bool `json:"coverage_confirmed"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	if b.Kind != "port" && b.Kind != "vlan" || b.Role != "primary" && b.Role != "observation" {
		return fail(400, "invalid_source", "Source kind or role is invalid")
	}
	if b.Direction == "" {
		b.Direction = "rx_upload"
	}
	if b.IntervalSeconds == 0 {
		b.IntervalSeconds = 60
	}
	if b.MaxBPS == "" {
		b.MaxBPS = "0"
	}
	if b.Direction != "rx_upload" && b.Direction != "rx_download" || b.IntervalSeconds < 10 || b.IntervalSeconds > 3600 || b.ExpectedIfIndex < 1 || strings.TrimSpace(b.IfName) == "" || len(b.IfName) > 255 || !trafficDomain.MatchString(b.NetworkDomain) || b.VLANID < 1 || b.VLANID > 4094 {
		return fail(400, "invalid_source", "Interface, topology, direction or interval is invalid")
	}
	if _, err := trafficNumber(b.MaxBPS, false); err != nil {
		return err
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	ac, err := trafficScanAccount(tx.QueryRow(r.Context(), `SELECT `+trafficAccountCols+` FROM traffic_accounts WHERE id=$1 FOR UPDATE`, b.AccountID))
	if err != nil {
		return dbError(err)
	}
	coverageVersions := map[string]int64{}
	if b.Kind == "port" {
		var device, owner, sid string
		var version, serverVersion, memberVersion int64
		var active bool
		err = tx.QueryRow(r.Context(), `SELECT b.device_id,b.version,b.active,s.owner_id,s.id,s.version,m.server_version FROM bindings b JOIN servers s ON s.id=b.server_id JOIN traffic_members m ON m.server_id=s.id AND m.account_id=$2 AND m.active WHERE b.id=$1 AND b.kind='network' FOR UPDATE OF b,s,m`, b.BindingID, b.AccountID).Scan(&device, &version, &active, &owner, &sid, &serverVersion, &memberVersion)
		if err != nil {
			return fail(409, "invalid_binding", "An active network binding of an account member is required")
		}
		if !active || owner != ac.OwnerID || serverVersion != memberVersion {
			return fail(409, "binding_changed", "Server ownership/version or binding changed; recreate membership before collecting")
		}
		b.DeviceID = device
		b.BindingVersion = version
		b.VLANMemberIDs = nil
	} else {
		b.BindingVersion = 0
		if b.BindingID != "" {
			return fail(400, "invalid_binding", "VLAN sources do not select a single server binding")
		}
		if !b.CoverageConfirmed || len(b.VLANMemberIDs) == 0 || len(b.VLANMemberIDs) > 100 {
			return fail(400, "coverage_required", "VLAN collection requires an explicitly confirmed complete server membership")
		}
		rows, e := tx.Query(r.Context(), `SELECT m.server_id FROM traffic_members m JOIN servers s ON s.id=m.server_id WHERE m.account_id=$1 AND m.active AND s.owner_id=$2 AND m.server_version=s.version`, b.AccountID, ac.OwnerID)
		if e != nil {
			return e
		}
		members := map[string]bool{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			members[id] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if b.Role == "primary" && len(members) != len(b.VLANMemberIDs) {
			return fail(409, "coverage_mismatch", "VLAN membership must exactly match this account")
		}
		sort.Strings(b.VLANMemberIDs)
		for _, sid := range b.VLANMemberIDs {
			if b.Role == "primary" && !members[sid] {
				return fail(409, "coverage_mismatch", "VLAN contains an unknown or other-account server")
			}
			delete(members, sid)
			if _, exists := coverageVersions[sid]; exists {
				return fail(400, "duplicate_member", "VLAN coverage contains a duplicate server")
			}
			var owner string
			var version int64
			if e = tx.QueryRow(r.Context(), `SELECT owner_id,version FROM servers WHERE id=$1 FOR SHARE`, sid).Scan(&owner, &version); e != nil {
				return fail(409, "coverage_mismatch", "VLAN contains an unknown server")
			}
			if owner != ac.OwnerID {
				return fail(409, "ownership_mismatch", "VLAN observations cannot include another customer's servers")
			}
			coverageVersions[sid] = version
		}
		var outside bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM traffic_sources src JOIN bindings binding ON binding.id=src.binding_id JOIN servers srv ON srv.id=binding.server_id WHERE src.active AND src.kind='port' AND src.network_domain=$1 AND src.vlan_id=$2 AND (NOT(srv.id=ANY($4::text[])) OR srv.owner_id<>$5 OR ($6='primary' AND src.account_id<>$3)))`, b.NetworkDomain, b.VLANID, b.AccountID, b.VLANMemberIDs, ac.OwnerID, b.Role).Scan(&outside)
		if err != nil {
			return err
		}
		if outside {
			return fail(409, "vlan_contains_other_account", "Known VLAN ports are outside confirmed customer/account coverage")
		}
	}
	var kind string
	if err = tx.QueryRow(r.Context(), `SELECT kind FROM devices WHERE id=$1 AND enabled`, b.DeviceID).Scan(&kind); err != nil {
		return fail(409, "invalid_device", "An enabled registered device is required")
	}
	if !b.Mock && kind != "snmp" {
		return fail(400, "invalid_device", "Real traffic collection requires an SNMP device")
	}
	b.ID = newID()
	b.Active = true
	_, err = tx.Exec(r.Context(), `INSERT INTO traffic_sources(id,account_id,device_id,binding_id,binding_version,kind,role,network_domain,vlan_id,if_name,expected_if_index,direction,interval_seconds,max_bps,mock,auto_collect) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, b.ID, b.AccountID, b.DeviceID, b.BindingID, b.BindingVersion, b.Kind, b.Role, b.NetworkDomain, b.VLANID, b.IfName, b.ExpectedIfIndex, b.Direction, b.IntervalSeconds, b.MaxBPS, b.Mock, b.AutoCollect)
	if err != nil {
		return dbError(err)
	}
	for _, sid := range b.VLANMemberIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO traffic_source_members(source_id,server_id,server_version) VALUES($1,$2,$3)`, b.ID, sid, coverageVersions[sid]); err != nil {
			return err
		}
	}
	if err = audit(r.Context(), tx, u, "traffic.source.create", b.ID, b); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 201, b.TrafficSource)
	return nil
}

func (a *App) retireTrafficSource(w http.ResponseWriter, r *http.Request, u User) error {
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	res, err := tx.Exec(r.Context(), `UPDATE traffic_sources SET active=false,ended_at=now(),auto_collect=false WHERE id=$1 AND active`, r.PathValue("id"))
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fail(404, "not_found", "Active traffic source not found")
	}
	if err = audit(r.Context(), tx, u, "traffic.source.retire", r.PathValue("id"), nil); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 200, map[string]bool{"retired": true})
	return nil
}

func (a *App) adjustTrafficAccount(w http.ResponseWriter, r *http.Request, u User) error {
	ac, err := a.trafficAccount(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	var b struct {
		Bytes          string    `json:"bytes"`
		Reason         string    `json:"reason"`
		PeriodStart    time.Time `json:"period_start"`
		IdempotencyKey string    `json:"idempotency_key"`
	}
	if err = decode(r, &b); err != nil {
		return err
	}
	if !trafficSignedDecimal.MatchString(b.Bytes) || strings.TrimSpace(b.Reason) == "" || len(b.Reason) > 500 || len(b.IdempotencyKey) < 8 || len(b.IdempotencyKey) > 120 {
		return fail(400, "invalid_adjustment", "A signed byte string, reason and 8–120 character idempotency key are required")
	}
	if b.PeriodStart.IsZero() {
		b.PeriodStart = trafficPeriod(time.Now(), ac)
	}
	if !b.PeriodStart.Equal(trafficPeriod(b.PeriodStart, ac)) {
		return fail(400, "invalid_period", "Adjustment must name an exact account period boundary")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var locked string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM traffic_accounts WHERE id=$1 FOR UPDATE`, ac.ID).Scan(&locked); err != nil {
		return err
	}
	var oldBytes, oldReason, id string
	var oldPeriod time.Time
	err = tx.QueryRow(r.Context(), `SELECT id,bytes::text,reason,period_start FROM traffic_adjustments WHERE account_id=$1 AND idempotency_key=$2`, ac.ID, b.IdempotencyKey).Scan(&id, &oldBytes, &oldReason, &oldPeriod)
	if err == nil {
		if oldBytes != b.Bytes || oldReason != b.Reason || !oldPeriod.Equal(b.PeriodStart) {
			return fail(409, "idempotency_conflict", "Key was used for a different adjustment")
		}
		jsonResponse(w, 200, map[string]string{"id": id})
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var quota string
	if err = tx.QueryRow(r.Context(), `SELECT (quota_bytes+COALESCE((SELECT SUM(bytes) FROM traffic_adjustments WHERE account_id=$1 AND period_start=$2),0)+$3::numeric)::text FROM traffic_accounts WHERE id=$1`, ac.ID, b.PeriodStart, b.Bytes).Scan(&quota); err != nil {
		return err
	}
	n, _ := new(big.Int).SetString(quota, 10)
	if n.Sign() < 0 {
		return fail(409, "negative_quota", "Adjustment would make the period quota negative")
	}
	id = newID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO traffic_adjustments(id,account_id,actor_id,period_start,bytes,reason,idempotency_key) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, ac.ID, u.ID, b.PeriodStart, b.Bytes, b.Reason, b.IdempotencyKey); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "traffic.quota.adjust", ac.ID, b); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 201, map[string]string{"id": id})
	return nil
}

func trafficSampleHash(s TrafficSample) string {
	s.ID = ""
	s.Quality = ""
	s.ObservedAt = s.ObservedAt.UTC()
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func trafficValidateSample(s TrafficSample) error {
	if len(s.SampleKey) < 1 || len(s.SampleKey) > 120 || s.ObservedAt.IsZero() || s.ObservedAt.After(time.Now().Add(5*time.Minute)) || s.ObservedAt.Year() < 2000 {
		return fail(400, "invalid_sample", "A sample key and valid observation timestamp are required")
	}
	if len(s.IfName) > 255 || len(s.IfAlias) > 512 || s.IfIndex < 1 || len(s.ErrorCode) > 80 {
		return fail(400, "invalid_sample", "Invalid interface metadata")
	}
	if s.LinkState != "up" && s.LinkState != "down" && s.LinkState != "unknown" {
		return fail(400, "invalid_sample", "link_state must be up, down or unknown")
	}
	for _, v := range []string{s.Uptime, s.Discontinuity} {
		if _, err := trafficNumber(v, true); err != nil {
			return err
		}
	}
	for _, v := range []string{s.InOctets, s.OutOctets} {
		if s.ErrorCode != "" && v == "" {
			continue
		}
		if _, err := trafficNumber(v, true); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) ingestTrafficSample(w http.ResponseWriter, r *http.Request, u User) error {
	var b TrafficSample
	if err := decode(r, &b); err != nil {
		return err
	}
	result, err := a.recordTrafficSample(r.Context(), r.PathValue("id"), b, &u, true)
	if err != nil {
		return err
	}
	jsonResponse(w, 201, result)
	return nil
}

func trafficSourceValid(ctx context.Context, tx pgx.Tx, s TrafficSource, owner string) error {
	query := `SELECT m.server_version,s.version,s.owner_id FROM traffic_members m JOIN servers s ON s.id=m.server_id WHERE m.account_id=$1 AND m.active ORDER BY s.id FOR SHARE OF m,s`
	id := s.AccountID
	if s.Kind == "vlan" {
		query = `SELECT m.server_version,s.version,s.owner_id FROM traffic_source_members m JOIN servers s ON s.id=m.server_id WHERE m.source_id=$1 ORDER BY s.id FOR SHARE OF m,s`
		id = s.ID
	}
	rows, err := tx.Query(ctx, query, id)
	if err != nil {
		return err
	}
	valid, count := true, 0
	for rows.Next() {
		var memberVersion, serverVersion int64
		var serverOwner string
		if err = rows.Scan(&memberVersion, &serverVersion, &serverOwner); err != nil {
			rows.Close()
			return err
		}
		count++
		valid = valid && memberVersion == serverVersion && serverOwner == owner
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	valid = valid && count > 0
	if !valid {
		return fail(409, "membership_changed", "Account membership or server ownership/version has changed; source is blocked")
	}
	if s.BindingID != "" {
		err = tx.QueryRow(ctx, `SELECT b.active AND b.version=$2 AND b.device_id=$3 AND b.kind='network' FROM bindings b JOIN traffic_members m ON m.server_id=b.server_id AND m.account_id=$4 AND m.active WHERE b.id=$1 FOR SHARE OF b`, s.BindingID, s.BindingVersion, s.DeviceID, s.AccountID).Scan(&valid)
		if errors.Is(err, pgx.ErrNoRows) {
			valid = false
		} else if err != nil {
			return err
		}
		if !valid {
			return fail(409, "binding_changed", "Historical network binding is no longer current; create a new source")
		}
	}
	return nil
}

// recordTrafficSample serializes on the source. This makes concurrent polling and replay
// use the same baseline and lets the database enforce deduplication across restarts.
func (a *App) recordTrafficSample(ctx context.Context, sourceID string, s TrafficSample, actor *User, manual bool) (map[string]any, error) {
	if err := trafficValidateSample(s); err != nil {
		return nil, err
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = trafficTopologyLock(ctx, tx, false); err != nil {
		return nil, err
	}
	src, err := trafficScanSource(tx.QueryRow(ctx, `SELECT `+trafficSourceCols+` FROM traffic_sources WHERE id=$1 FOR UPDATE`, sourceID))
	if err != nil {
		return nil, dbError(err)
	}
	if !src.Active {
		return nil, fail(409, "source_retired", "Cannot collect a retired source")
	}
	if manual && !src.Mock {
		return nil, fail(409, "manual_sample_forbidden", "Manual samples are restricted to explicitly mock sources")
	}
	ac, err := trafficScanAccount(tx.QueryRow(ctx, `SELECT `+trafficAccountCols+` FROM traffic_accounts WHERE id=$1 FOR UPDATE`, src.AccountID))
	if err != nil {
		return nil, err
	}
	if err = trafficSourceValid(ctx, tx, src, ac.OwnerID); err != nil {
		return nil, err
	}
	hash := trafficSampleHash(s)
	var existing, oldHash, oldQuality string
	err = tx.QueryRow(ctx, `SELECT id,payload_hash,quality FROM traffic_samples WHERE source_id=$1 AND (sample_key=$2 OR observed_at=$3)`, sourceID, s.SampleKey, s.ObservedAt).Scan(&existing, &oldHash, &oldQuality)
	if err == nil {
		if oldHash != hash {
			return nil, fail(409, "sample_conflict", "Sample key or observation time already has different data")
		}
		return map[string]any{"id": existing, "quality": oldQuality, "duplicate": true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var latest time.Time
	err = tx.QueryRow(ctx, `SELECT observed_at FROM traffic_samples WHERE source_id=$1 ORDER BY observed_at DESC LIMIT 1`, sourceID).Scan(&latest)
	if err == nil && !s.ObservedAt.After(latest) {
		return nil, fail(409, "out_of_order", "Late samples are rejected; retained ledger cannot be silently rewritten")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	prev, e := trafficScanSample(tx.QueryRow(ctx, `SELECT `+trafficSampleCols+` FROM traffic_samples WHERE source_id=$1 AND error_code='' ORDER BY observed_at DESC LIMIT 1`, sourceID))
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	var interval *TrafficInterval
	s.Quality = "baseline"
	if s.ErrorCode != "" {
		s.Quality = "collection_failed"
	} else if s.BindingVersion != src.BindingVersion {
		s.Quality = "binding_changed"
		s.ErrorCode = "binding_changed"
	} else if s.IfName != src.IfName || s.IfIndex != src.ExpectedIfIndex {
		s.Quality = "interface_remapped"
		s.ErrorCode = "interface_remapped"
	} else if e == nil {
		var missed, identityChanged bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM traffic_samples WHERE source_id=$1 AND observed_at>$2 AND error_code<>''),EXISTS(SELECT 1 FROM traffic_samples WHERE source_id=$1 AND observed_at>$2 AND error_code IN ('interface_remapped','binding_changed'))`, sourceID, prev.ObservedAt).Scan(&missed, &identityChanged); err != nil {
			return nil, err
		}
		if identityChanged {
			s.Quality = "baseline_after_identity_change"
		} else {
			evaluated := evaluateTraffic(prev, s, src, ac, missed)
			interval = &evaluated
			s.Quality = evaluated.Quality
		}
	}
	s.ID = newID()
	_, err = tx.Exec(ctx, `INSERT INTO traffic_samples(id,source_id,sample_key,observed_at,in_octets,out_octets,if_index,if_name,if_alias,uptime,discontinuity,link_state,binding_version,quality,error_code,payload_hash) VALUES($1,$2,$3,$4,NULLIF($5,'')::numeric,NULLIF($6,'')::numeric,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, s.ID, sourceID, s.SampleKey, s.ObservedAt, s.InOctets, s.OutOctets, s.IfIndex, s.IfName, s.IfAlias, s.Uptime, s.Discontinuity, s.LinkState, s.BindingVersion, s.Quality, s.ErrorCode, hash)
	if err != nil {
		return nil, dbError(err)
	}
	if interval != nil {
		_, err = tx.Exec(ctx, `INSERT INTO traffic_ledger(id,account_id,source_id,start_sample_id,end_sample_id,starts_at,ends_at,period_start,upload_bytes,download_bytes,quality,billable) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10::numeric,$11,$12)`, newID(), src.AccountID, sourceID, prev.ID, s.ID, interval.StartsAt, interval.EndsAt, interval.PeriodStart, interval.UploadBytes, interval.DownloadBytes, interval.Quality, interval.Billable)
		if err != nil {
			return nil, err
		}
		if interval.Billable {
			if err = trafficCheckAlerts(ctx, tx, ac, *interval.PeriodStart); err != nil {
				return nil, err
			}
		}
	}
	if actor != nil {
		if err = audit(ctx, tx, *actor, "traffic.sample.record", s.ID, map[string]any{"source_id": sourceID, "quality": s.Quality}); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"id": s.ID, "quality": s.Quality, "duplicate": false, "interval": interval}, nil
}

const trafficSampleCols = `id,sample_key,observed_at,COALESCE(in_octets::text,''),COALESCE(out_octets::text,''),if_index,if_name,if_alias,uptime::text,discontinuity::text,link_state,binding_version,quality,error_code`

func trafficScanSample(row pgx.Row) (TrafficSample, error) {
	var s TrafficSample
	err := row.Scan(&s.ID, &s.SampleKey, &s.ObservedAt, &s.InOctets, &s.OutOctets, &s.IfIndex, &s.IfName, &s.IfAlias, &s.Uptime, &s.Discontinuity, &s.LinkState, &s.BindingVersion, &s.Quality, &s.ErrorCode)
	return s, err
}

func trafficCheckAlerts(ctx context.Context, tx pgx.Tx, ac TrafficAccount, period time.Time) error {
	if ac.AlertPercent == 0 {
		return nil
	}
	var used, quota string
	err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE $3 WHEN 'upload' THEN upload_bytes WHEN 'download' THEN download_bytes ELSE upload_bytes+download_bytes END),0)::text FROM traffic_ledger WHERE account_id=$1 AND period_start=$2 AND billable`, ac.ID, period, ac.Direction).Scan(&used)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT (quota_bytes+COALESCE((SELECT SUM(bytes) FROM traffic_adjustments WHERE account_id=$1 AND period_start=$2),0))::text FROM traffic_accounts WHERE id=$1`, ac.ID, period).Scan(&quota)
	if err != nil {
		return err
	}
	usedN, _ := new(big.Int).SetString(used, 10)
	quotaN, _ := new(big.Int).SetString(quota, 10)
	if quotaN.Sign() == 0 {
		return nil
	}
	scaled := new(big.Int).Mul(usedN, big.NewInt(100))
	threshold := new(big.Int).Mul(quotaN, big.NewInt(int64(ac.AlertPercent)))
	if scaled.Cmp(threshold) < 0 {
		return nil
	}
	kind := "threshold"
	if usedN.Cmp(quotaN) >= 0 {
		kind = "over_quota"
	}
	_, err = tx.Exec(ctx, `INSERT INTO traffic_alerts(id,account_id,period_start,kind) VALUES($1,$2,$3,$4) ON CONFLICT(account_id,period_start,kind) DO NOTHING`, newID(), ac.ID, period, kind)
	return err
}

func (a *App) getTrafficAccount(w http.ResponseWriter, r *http.Request, u User) error {
	ac, err := a.trafficAccount(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	at := time.Now()
	if value := r.URL.Query().Get("at"); value != "" {
		at, err = time.Parse(time.RFC3339, value)
		if err != nil {
			return fail(400, "invalid_period", "at must be an RFC3339 timestamp")
		}
	}
	period := trafficPeriod(at, ac)
	next := trafficNextPeriod(period, ac)
	var upload, download, adjusted, pendingUp, pendingDown string
	var knownIntervals, unknownIntervals int
	err = a.DB.QueryRow(r.Context(), `SELECT COALESCE(SUM(upload_bytes) FILTER(WHERE billable AND period_start=$2),0)::text,COALESCE(SUM(download_bytes) FILTER(WHERE billable AND period_start=$2),0)::text,COALESCE(SUM(upload_bytes) FILTER(WHERE quality='boundary_unallocated' AND source_id IN(SELECT id FROM traffic_sources WHERE role='primary') AND ends_at>$2 AND starts_at<$3),0)::text,COALESCE(SUM(download_bytes) FILTER(WHERE quality='boundary_unallocated' AND source_id IN(SELECT id FROM traffic_sources WHERE role='primary') AND ends_at>$2 AND starts_at<$3),0)::text FROM traffic_ledger WHERE account_id=$1`, ac.ID, period, next).Scan(&upload, &download, &pendingUp, &pendingDown)
	if err != nil {
		return err
	}
	err = a.DB.QueryRow(r.Context(), `SELECT (quota_bytes+COALESCE((SELECT SUM(bytes) FROM traffic_adjustments WHERE account_id=$1 AND period_start=$2),0))::text FROM traffic_accounts WHERE id=$1`, ac.ID, period).Scan(&adjusted)
	if err != nil {
		return err
	}
	up, _ := new(big.Int).SetString(upload, 10)
	down, _ := new(big.Int).SetString(download, 10)
	quota, _ := new(big.Int).SetString(adjusted, 10)
	used := new(big.Int).Add(up, down)
	if ac.Direction == "upload" {
		used = up
	}
	if ac.Direction == "download" {
		used = down
	}
	remaining := new(big.Int).Sub(quota, used)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	sources := []TrafficSource{}
	visibleSourceIDs := []string{}
	rows, err := a.DB.Query(r.Context(), `SELECT `+trafficSourceCols+` FROM traffic_sources WHERE account_id=$1 AND ($2 OR kind<>'vlan' OR role<>'observation' OR (EXISTS(SELECT 1 FROM traffic_source_members m WHERE m.source_id=traffic_sources.id) AND NOT EXISTS(SELECT 1 FROM traffic_source_members m JOIN servers srv ON srv.id=m.server_id WHERE m.source_id=traffic_sources.id AND (srv.owner_id<>$3 OR srv.version<>m.server_version)))) ORDER BY created_at`, ac.ID, u.Role == "admin" || u.Role == "operator", ac.OwnerID)
	if err != nil {
		return err
	}
	for rows.Next() {
		s, e := trafficScanSource(rows)
		if e != nil {
			rows.Close()
			return e
		}
		sources = append(sources, s)
		visibleSourceIDs = append(visibleSourceIDs, s.ID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = a.DB.Query(r.Context(), `SELECT source_id,server_id FROM traffic_source_members WHERE source_id=ANY($1::text[]) ORDER BY server_id`, visibleSourceIDs)
	if err != nil {
		return err
	}
	coverage := map[string][]string{}
	for rows.Next() {
		var source, server string
		if err = rows.Scan(&source, &server); err != nil {
			rows.Close()
			return err
		}
		coverage[source] = append(coverage[source], server)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i := range sources {
		sources[i].VLANMemberIDs = coverage[sources[i].ID]
	}
	err = a.DB.QueryRow(r.Context(), `SELECT COUNT(*) FILTER(WHERE billable),COUNT(*) FILTER(WHERE quality NOT IN ('normal','recovered_gap')) FROM traffic_ledger WHERE account_id=$1 AND ends_at>$2 AND starts_at<$3 AND source_id=ANY($4::text[])`, ac.ID, period, next, visibleSourceIDs).Scan(&knownIntervals, &unknownIntervals)
	if err != nil {
		return err
	}
	intervals := []TrafficInterval{}
	rows, err = a.DB.Query(r.Context(), `SELECT source_id,starts_at,ends_at,period_start,upload_bytes::text,download_bytes::text,quality,billable FROM traffic_ledger WHERE account_id=$1 AND ends_at>$2 AND starts_at<$3 AND source_id=ANY($4::text[]) ORDER BY ends_at DESC LIMIT 2000`, ac.ID, period, next, visibleSourceIDs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var point TrafficInterval
		if err = rows.Scan(&point.SourceID, &point.StartsAt, &point.EndsAt, &point.PeriodStart, &point.UploadBytes, &point.DownloadBytes, &point.Quality, &point.Billable); err != nil {
			rows.Close()
			return err
		}
		intervals = append(intervals, point)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	samples := []map[string]any{}
	rows, err = a.DB.Query(r.Context(), `SELECT source_id,observed_at,quality,error_code FROM traffic_samples WHERE source_id=ANY($1::text[]) ORDER BY observed_at DESC LIMIT 100`, visibleSourceIDs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var source, quality, code string
		var t time.Time
		if err = rows.Scan(&source, &t, &quality, &code); err != nil {
			rows.Close()
			return err
		}
		samples = append(samples, map[string]any{"source_id": source, "observed_at": t, "quality": quality, "error_code": code})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	members := []map[string]any{}
	rows, err = a.DB.Query(r.Context(), `SELECT s.id,s.name,(s.owner_id=$2 AND s.version=m.server_version) FROM traffic_members m JOIN servers s ON s.id=m.server_id WHERE m.account_id=$1 AND m.active AND ($3 OR s.owner_id=$2) ORDER BY s.name`, ac.ID, ac.OwnerID, u.Role == "admin" || u.Role == "operator")
	if err != nil {
		return err
	}
	for rows.Next() {
		var sid, name string
		var valid bool
		if err = rows.Scan(&sid, &name, &valid); err != nil {
			rows.Close()
			return err
		}
		members = append(members, map[string]any{"id": sid, "name": name, "valid": valid})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	alerts := []map[string]any{}
	rows, err = a.DB.Query(r.Context(), `SELECT kind,created_at FROM traffic_alerts WHERE account_id=$1 AND period_start=$2 ORDER BY created_at`, ac.ID, period)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind string
		var t time.Time
		if err = rows.Scan(&kind, &t); err != nil {
			rows.Close()
			return err
		}
		alerts = append(alerts, map[string]any{"kind": kind, "created_at": t})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Customer responses expose traffic data, not management-plane device and port identifiers.
	if u.Role != "admin" && u.Role != "operator" {
		for i := range sources {
			sources[i].DeviceID = ""
			sources[i].BindingID = ""
			sources[i].IfName = ""
			sources[i].ExpectedIfIndex = 0
			sources[i].NetworkDomain = ""
			sources[i].VLANID = 0
			sources[i].VLANMemberIDs = nil
		}
	}
	jsonResponse(w, 200, map[string]any{"account": ac, "period_start": period, "period_end": next, "upload_bytes": upload, "download_bytes": download, "used_bytes": used.String(), "effective_quota_bytes": adjusted, "remaining_bytes": remaining.String(), "over_quota": used.Cmp(quota) > 0, "unallocated_upload_bytes": pendingUp, "unallocated_download_bytes": pendingDown, "sources": sources, "members": members, "intervals": intervals, "samples": samples, "alerts": alerts, "chart_limit": 2000, "enforcement_enabled": false, "known_intervals": knownIntervals, "unknown_intervals": unknownIntervals, "coverage_note": "Totals include confirmed intervals only; missing data is not zero and full billing-period coverage is not assumed"})
	return nil
}

func mustTrafficLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func trafficFailureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "collection_timeout"
	}
	return "collection_failed"
}

func (a *App) getServerTraffic(w http.ResponseWriter, r *http.Request, u User) error {
	server, err := a.server(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	var accountID string
	err = a.DB.QueryRow(r.Context(), `SELECT m.account_id FROM traffic_members m JOIN traffic_accounts a ON a.id=m.account_id WHERE m.server_id=$1 AND m.active AND a.owner_id=$2 AND m.server_version=$3`, server.ID, server.OwnerID, server.Version).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "traffic_account_missing", "Server has no current traffic account")
	}
	if err != nil {
		return err
	}
	r.SetPathValue("id", accountID)
	return a.getTrafficAccount(w, r, u)
}

func (a *App) retireTrafficAccount(w http.ResponseWriter, r *http.Request, u User) error {
	ac, err := a.trafficAccount(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_sources SET active=false,ended_at=now(),auto_collect=false WHERE account_id=$1 AND active`, ac.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_members SET active=false,ended_at=now() WHERE account_id=$1 AND active`, ac.ID); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "traffic.account.retire", ac.ID, map[string]any{"history_preserved": true}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 200, map[string]bool{"retired": true})
	return nil
}

// Explicitly accepting a changed server version never carries its old baseline to
// new bindings. Account quota and accumulated usage remain in the same history.
func (a *App) refreshTrafficMember(w http.ResponseWriter, r *http.Request, u User) error {
	ac, err := a.trafficAccount(r.Context(), r.PathValue("id"), u)
	if err != nil {
		return err
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	// Lock sources before the account, matching the ingestion order.
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_sources SET active=false,ended_at=now(),auto_collect=false WHERE account_id=$1 AND active AND (kind='vlan' OR binding_id IN(SELECT id FROM bindings WHERE server_id=$2))`, ac.ID, r.PathValue("server_id")); err != nil {
		return err
	}
	var owner string
	var version, oldVersion int64
	err = tx.QueryRow(r.Context(), `SELECT s.owner_id,s.version,m.server_version FROM servers s JOIN traffic_members m ON m.server_id=s.id WHERE s.id=$1 AND m.account_id=$2 AND m.active FOR UPDATE OF s,m`, r.PathValue("server_id"), ac.ID).Scan(&owner, &version, &oldVersion)
	if err != nil {
		return dbError(err)
	}
	if owner != ac.OwnerID {
		return fail(409, "ownership_mismatch", "A reassigned server cannot be refreshed into its previous owner's account")
	}
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_members SET server_version=$3 WHERE account_id=$1 AND server_id=$2 AND active`, ac.ID, r.PathValue("server_id"), version); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "traffic.member.refresh", r.PathValue("server_id"), map[string]any{"account_id": ac.ID, "previous_version": oldVersion, "new_version": version, "sources_retired": true}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	jsonResponse(w, 200, map[string]bool{"refreshed": true, "sources_retired": true})
	return nil
}

func (a *App) addTrafficMember(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		ServerID      string `json:"server_id"`
		ServerVersion int64  `json:"server_version"`
		Reason        string `json:"reason"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	if b.ServerID == "" || b.ServerVersion < 1 || strings.TrimSpace(b.Reason) == "" || len(b.Reason) > 500 {
		return fail(400, "invalid_member", "Server, current version and a 1–500 UTF-8 byte reason are required")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	ac, err := trafficScanAccount(tx.QueryRow(r.Context(), `SELECT `+trafficAccountCols+` FROM traffic_accounts WHERE id=$1 FOR UPDATE`, r.PathValue("id")))
	if err != nil {
		return dbError(err)
	}
	var owner string
	var version int64
	if err = tx.QueryRow(r.Context(), `SELECT owner_id,version FROM servers WHERE id=$1 FOR UPDATE`, b.ServerID).Scan(&owner, &version); err != nil {
		return dbError(err)
	}
	if owner != ac.OwnerID {
		return fail(409, "ownership_mismatch", "Members must have the same owner as the traffic account")
	}
	if version != b.ServerVersion {
		return fail(409, "membership_changed", "Reload the server before confirming membership")
	}
	var existing string
	err = tx.QueryRow(r.Context(), `SELECT account_id FROM traffic_members WHERE server_id=$1 AND active`, b.ServerID).Scan(&existing)
	if err == nil {
		return fail(409, "active_member", "Server already belongs to an active traffic account")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM traffic_members WHERE account_id=$1 AND active`, ac.ID).Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return fail(409, "member_limit", "Traffic accounts support at most 100 active members")
	}
	// A new member changes complete VLAN coverage. Reconfirm it in a new source;
	// existing per-port baselines and historical ledger entries remain unchanged.
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_sources SET active=false,ended_at=now(),auto_collect=false WHERE account_id=$1 AND active AND kind='vlan'`, ac.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO traffic_members(account_id,server_id,server_version) VALUES($1,$2,$3)`, ac.ID, b.ServerID, version); err != nil {
		return dbError(err)
	}
	if err = audit(r.Context(), tx, u, "traffic.member.add", b.ServerID, map[string]any{"account_id": ac.ID, "server_version": version, "reason": b.Reason, "history_preserved": true}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return dbError(err)
	}
	jsonResponse(w, 201, map[string]any{"account_id": ac.ID, "server_id": b.ServerID, "server_version": version, "changed": true})
	return nil
}

func (a *App) removeTrafficMember(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &b); err != nil {
		return err
	}
	if strings.TrimSpace(b.Reason) == "" || len(b.Reason) > 500 {
		return fail(400, "invalid_member", "A 1–500 UTF-8 byte reason is required")
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err = trafficTopologyLock(r.Context(), tx, true); err != nil {
		return err
	}
	var account string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM traffic_accounts WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&account); err != nil {
		return dbError(err)
	}
	server := r.PathValue("server_id")
	var id int64
	var active bool
	if err = tx.QueryRow(r.Context(), `SELECT id,active FROM traffic_members WHERE account_id=$1 AND server_id=$2 ORDER BY id DESC LIMIT 1 FOR UPDATE`, account, server).Scan(&id, &active); err != nil {
		return dbError(err)
	}
	if !active {
		jsonResponse(w, 200, map[string]bool{"removed": true, "changed": false, "history_preserved": true})
		return nil
	}
	// Retire account-wide VLAN totals and any cross-account observation that
	// covered the departing server. A new source needs a fresh baseline.
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_sources SET active=false,ended_at=now(),auto_collect=false WHERE active AND ((account_id=$1 AND (kind='vlan' OR binding_id IN(SELECT id FROM bindings WHERE server_id=$2))) OR id IN(SELECT source_id FROM traffic_source_members WHERE server_id=$2))`, account, server); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE traffic_members SET active=false,ended_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if err = audit(r.Context(), tx, u, "traffic.member.remove", server, map[string]any{"account_id": account, "reason": b.Reason, "history_preserved": true}); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return dbError(err)
	}
	jsonResponse(w, 200, map[string]bool{"removed": true, "changed": true, "history_preserved": true})
	return nil
}
