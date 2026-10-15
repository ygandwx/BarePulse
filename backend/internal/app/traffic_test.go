package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ygandwx/BarePulse/backend/migrations"
)

func trafficTestTime() time.Time {
	n := time.Now().UTC().AddDate(0, -1, 0)
	return time.Date(n.Year(), n.Month(), 15, 12, 0, 0, 0, time.UTC)
}
func trafficTestSample(at time.Time, key, in, out string) TrafficSample {
	return TrafficSample{SampleKey: key, ObservedAt: at, InOctets: in, OutOctets: out, IfIndex: 7, IfName: "mock-eth7", Uptime: "10000", Discontinuity: "0", LinkState: "up", BindingVersion: 1}
}

func TestTrafficCounterQualityAndPrecision(t *testing.T) {
	ac := TrafficAccount{CycleDay: 1, Timezone: "UTC"}
	src := TrafficSource{ID: "source", BindingVersion: 1, IfName: "mock-eth7", ExpectedIfIndex: 7, Role: "primary", Direction: "rx_upload", IntervalSeconds: 60, MaxBPS: "0"}
	prev := trafficTestSample(trafficTestTime(), "a", "9007199254740993", "18014398509481986")
	cur := trafficTestSample(prev.ObservedAt.Add(time.Minute), "b", "9007199254740994", "18014398509482009")
	cur.Uptime = "16000"
	tests := []struct {
		name, want string
		mutate     func(*TrafficSample, *TrafficSource)
		missed     bool
	}{
		{"exact", "normal", func(*TrafficSample, *TrafficSource) {}, false},
		{"counter reset", "counter_reset", func(s *TrafficSample, _ *TrafficSource) { s.InOctets = "10" }, false},
		{"reboot", "uptime_reset", func(s *TrafficSample, _ *TrafficSource) { s.Uptime = "1" }, false},
		{"discontinuity", "counter_discontinuity", func(s *TrafficSample, _ *TrafficSource) { s.Discontinuity = "100" }, false},
		{"index", "interface_remapped", func(s *TrafficSample, _ *TrafficSource) { s.IfIndex++ }, false},
		{"name", "interface_remapped", func(s *TrafficSample, _ *TrafficSource) { s.IfName = "new" }, false},
		{"binding", "binding_changed", func(s *TrafficSample, _ *TrafficSource) { s.BindingVersion++ }, false},
		{"missing", "recovered_gap", func(*TrafficSample, *TrafficSource) {}, true},
		{"gap duration", "recovered_gap", func(s *TrafficSample, _ *TrafficSource) { s.ObservedAt = s.ObservedAt.Add(time.Minute) }, false},
		{"jump", "implausible_jump", func(_ *TrafficSample, s *TrafficSource) { s.MaxBPS = "1" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, source := cur, src
			tc.mutate(&s, &source)
			got := evaluateTraffic(prev, s, source, ac, tc.missed)
			if got.Quality != tc.want {
				t.Fatalf("quality %s expected %s", got.Quality, tc.want)
			}
			if tc.want == "normal" || tc.want == "recovered_gap" {
				if got.UploadBytes == nil || *got.UploadBytes != "1" || *got.DownloadBytes != "23" || !got.Billable {
					t.Fatalf("lost exact bytes: %+v", got)
				}
			} else if got.UploadBytes != nil || got.Billable {
				t.Fatalf("unknown interval billed: %+v", got)
			}
		})
	}
	src.Direction = "rx_download"
	got := evaluateTraffic(prev, cur, src, ac, false)
	if *got.UploadBytes != "23" || *got.DownloadBytes != "1" {
		t.Fatal("direction mapping")
	}
	src.Role = "observation"
	if evaluateTraffic(prev, cur, src, ac, false).Billable {
		t.Fatal("observation billed")
	}
	if _, err := trafficNumber("18446744073709551615", true); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"18446744073709551616", "1e9", "-1", "9007199254740993.0", " 100"} {
		if _, err := trafficNumber(bad, true); err == nil {
			t.Fatalf("invalid counter %s accepted", bad)
		}
	}
}

func TestTrafficCycleBoundaryIsNotInvented(t *testing.T) {
	ac := TrafficAccount{CycleDay: 15, Timezone: "Asia/Shanghai"}
	src := TrafficSource{BindingVersion: 1, IfName: "mock-eth7", ExpectedIfIndex: 7, Role: "primary", IntervalSeconds: 60, MaxBPS: "0"}
	boundary := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	prev := trafficTestSample(boundary.Add(-30*time.Second), "a", "10", "20")
	cur := trafficTestSample(boundary.Add(30*time.Second), "b", "110", "220")
	got := evaluateTraffic(prev, cur, src, ac, false)
	if got.Quality != "boundary_unallocated" || got.Billable || got.PeriodStart != nil || *got.UploadBytes != "100" {
		t.Fatalf("cross-cycle delta changed: %+v", got)
	}
	cur.ObservedAt = boundary
	got = evaluateTraffic(prev, cur, src, ac, false)
	if !got.Billable || !got.PeriodStart.Equal(boundary.AddDate(0, -1, 0)) {
		t.Fatal("exact boundary belongs to completed interval")
	}
	ac.Timezone = "America/New_York"
	ac.CycleDay = 1
	start := trafficPeriod(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), ac)
	end := start.In(mustTrafficLocation(ac.Timezone)).AddDate(0, 1, 0).UTC()
	if end.Sub(start) != 31*24*time.Hour-time.Hour {
		t.Fatalf("DST month boundary wrong: %s", end.Sub(start))
	}
	ac = TrafficAccount{CycleDay: 31, Timezone: "UTC"}
	start = trafficPeriod(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), ac)
	if !start.Equal(time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)) || !trafficNextPeriod(start, ac).Equal(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("anniversary short-month clamp lost cycle anchor")
	}
}

func TestTrafficSNMPReadOnlyContract(t *testing.T) {
	src := TrafficSource{ExpectedIfIndex: 7, IfName: "eth7", BindingVersion: 3}
	oids := trafficOIDs(7)
	packet := &gosnmp.SnmpPacket{Error: gosnmp.NoError, Variables: []gosnmp.SnmpPDU{
		{Name: oids[0], Type: gosnmp.TimeTicks, Value: uint32(12345)}, {Name: oids[1], Type: gosnmp.OctetString, Value: []byte("eth7")}, {Name: oids[2], Type: gosnmp.Counter64, Value: uint64(18446744073709551615)}, {Name: oids[3], Type: gosnmp.Counter64, Value: uint64(9007199254740993)}, {Name: oids[4], Type: gosnmp.TimeTicks, Value: uint32(0)}, {Name: oids[5], Type: gosnmp.OctetString, Value: []byte("customer link")}, {Name: oids[6], Type: gosnmp.Integer, Value: 1}}}
	sample, err := trafficParseSNMP(packet, src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sample.InOctets != "18446744073709551615" || sample.OutOctets != "9007199254740993" || sample.LinkState != "up" {
		t.Fatalf("SNMP precision: %+v", sample)
	}
	packet.Variables[2].Type = gosnmp.Counter32
	if _, err = trafficParseSNMP(packet, src, time.Now()); err == nil {
		t.Fatal("32-bit fallback silently accepted")
	}
	packet.Variables = packet.Variables[:2]
	if _, err = trafficParseSNMP(packet, src, time.Now()); err == nil {
		t.Fatal("incomplete response accepted")
	}
	collector := SNMPTrafficCollector{Address: "must-never-resolve.invalid"}
	if _, err = collector.Collect(context.Background(), src); err == nil {
		t.Fatal("network gate bypassed")
	}
	if _, err = collector.Discover(context.Background()); err == nil {
		t.Fatal("discovery gate bypassed")
	}
}

type trafficFixture struct {
	a                                                 *App
	admin, owner, other                               User
	server, server2, device, binding, account, source string
}

func newTrafficFixture(t *testing.T) trafficFixture {
	t.Helper()
	f := trafficFixture{a: testApp(t), admin: User{ID: newID(), Role: "admin"}, owner: User{ID: newID(), Role: "customer"}, other: User{ID: newID(), Role: "customer"}, server: newID(), server2: newID(), device: newID(), binding: newID()}
	ctx := context.Background()
	for _, u := range []User{f.admin, f.owner, f.other} {
		if _, err := f.a.DB.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,'unused',$3)`, u.ID, u.ID+"@example.test", u.Role); err != nil {
			t.Fatal(err)
		}
	}
	for _, sid := range []string{f.server, f.server2} {
		if _, err := f.a.DB.Exec(ctx, `INSERT INTO servers(id,name,owner_id) VALUES($1,$1,$2)`, sid, f.owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO devices(id,name,kind) VALUES($1,$1,'mock_bmc')`, f.device); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES($1,$2,$3,'network',7)`, f.binding, f.server, f.device); err != nil {
		t.Fatal(err)
	}
	response := testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": "Shared", "owner_id": f.owner.ID, "quota_bytes": "1000", "direction": "both", "timezone": "UTC", "cycle_day": 1, "alert_percent": 80, "server_ids": []string{f.server, f.server2}}, f.admin)
	f.account = trafficResponseID(t, response, 201)
	response = testRequest(t, f.a, "POST", "/api/v1/traffic/sources", map[string]any{"account_id": f.account, "binding_id": f.binding, "kind": "port", "role": "primary", "network_domain": "lab", "vlan_id": 100, "if_name": "mock-eth7", "expected_if_index": 7, "mock": true}, f.admin)
	f.source = trafficResponseID(t, response, 201)
	return f
}
func trafficResponseID(t *testing.T, response *httptest.ResponseRecorder, status int) string {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status %d want %d: %s", response.Code, status, response.Body.String())
	}
	var b map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	id, _ := b["id"].(string)
	if id == "" {
		t.Fatalf("id missing %s", response.Body)
	}
	return id
}
func trafficGetDetail(t *testing.T, f trafficFixture, at time.Time) map[string]any {
	t.Helper()
	r := testRequest(t, f.a, "GET", "/api/v1/traffic/accounts/"+f.account+"?at="+at.UTC().Format(time.RFC3339), nil, f.owner)
	if r.Code != 200 {
		t.Fatalf("detail %d %s", r.Code, r.Body)
	}
	var data map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTrafficPGAccountingReplayOwnershipAndAdjustments(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	first := trafficTestSample(trafficTestTime(), "baseline", "9007199254740993", "18014398509481986")
	if _, err := f.a.recordTrafficSample(ctx, f.source, first, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	second := trafficTestSample(first.ObservedAt.Add(time.Minute), "second", "9007199254741394", "18014398509482585")
	second.Uptime = "16000"
	errs := make(chan error, 12)
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := f.a.recordTrafficSample(ctx, f.source, second, &f.admin, true)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var samples, ledger int
	if err := f.a.DB.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM traffic_samples),(SELECT COUNT(*) FROM traffic_ledger)`).Scan(&samples, &ledger); err != nil {
		t.Fatal(err)
	}
	if samples != 2 || ledger != 1 {
		t.Fatalf("duplicate accounting samples=%d ledger=%d", samples, ledger)
	}
	detail := trafficGetDetail(t, f, first.ObservedAt)
	if detail["upload_bytes"] != "401" || detail["download_bytes"] != "599" || detail["remaining_bytes"] != "0" {
		t.Fatalf("wrong totals %+v", detail)
	}
	if strings.Contains(fmt.Sprint(detail["sources"]), f.device) {
		t.Fatal("customer sees management device ID")
	}
	serverTraffic := testRequest(t, f.a, "GET", "/api/v1/servers/"+f.server+"/traffic", nil, f.owner)
	if serverTraffic.Code != 200 {
		t.Fatalf("customer server traffic %d %s", serverTraffic.Code, serverTraffic.Body)
	}
	serverTraffic = testRequest(t, f.a, "GET", "/api/v1/servers/"+f.server+"/traffic", nil, f.other)
	if serverTraffic.Code != 404 {
		t.Fatal("cross-owner server traffic allowed")
	}
	r := testRequest(t, f.a, "GET", "/api/v1/traffic/accounts/"+f.account, nil, f.other)
	if r.Code != 404 {
		t.Fatalf("other customer access %d", r.Code)
	}
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources/"+f.source+"/collect", map[string]string{}, f.owner)
	if r.Code != 403 {
		t.Fatal("customer can inject samples")
	}
	changed := second
	changed.InOctets = "9007199254741494"
	if _, err := f.a.recordTrafficSample(ctx, f.source, changed, &f.admin, true); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	adjustment := map[string]any{"bytes": "500", "reason": "offline package fixture", "period_start": trafficPeriod(first.ObservedAt, TrafficAccount{Timezone: "UTC", CycleDay: 1}), "idempotency_key": "package-one"}
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/adjustments", adjustment, f.admin)
	id := trafficResponseID(t, r, 201)
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/adjustments", adjustment, f.admin)
	if trafficResponseID(t, r, 200) != id {
		t.Fatal("adjustment replay changed ID")
	}
	detail = trafficGetDetail(t, f, first.ObservedAt)
	if detail["effective_quota_bytes"] != "1500" || detail["remaining_bytes"] != "500" {
		t.Fatal("adjustment did not apply")
	}
	adjustment["bytes"] = "501"
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/adjustments", adjustment, f.admin)
	if r.Code != 409 {
		t.Fatal("adjustment conflicting replay accepted")
	}
	adjustment["bytes"] = "-9999"
	adjustment["idempotency_key"] = "refund-two"
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/adjustments", adjustment, f.admin)
	if r.Code != 409 {
		t.Fatal("negative quota accepted")
	}
	if _, err := f.a.DB.Exec(ctx, `UPDATE servers SET owner_id=$2,version=version+1 WHERE id=$1`, f.server, f.other.ID); err != nil {
		t.Fatal(err)
	}
	third := second
	third.SampleKey = "third"
	third.ObservedAt = second.ObservedAt.Add(time.Minute)
	if _, err := f.a.recordTrafficSample(ctx, f.source, third, &f.admin, true); err == nil {
		t.Fatal("ownership drift billed")
	}
	detail = trafficGetDetail(t, f, first.ObservedAt)
	if len(detail["members"].([]any)) != 1 {
		t.Fatal("old owner sees reassigned server")
	}
}

func TestTrafficPGObservationOverlapAndBindingDrift(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	sourceBody := map[string]any{"account_id": f.account, "device_id": f.device, "kind": "vlan", "role": "primary", "network_domain": "lab", "vlan_id": 100, "if_name": "vlan100", "expected_if_index": 100, "mock": true, "coverage_confirmed": true, "vlan_member_ids": []string{f.server, f.server2}}
	r := testRequest(t, f.a, "POST", "/api/v1/traffic/sources", sourceBody, f.admin)
	if r.Code != 409 {
		t.Fatalf("port/VLAN overlap accepted %d %s", r.Code, r.Body)
	}
	sourceBody["role"] = "observation"
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources", sourceBody, f.admin)
	observation := trafficResponseID(t, r, 201)
	base := trafficTestSample(trafficTestTime(), "vlan-first", "1", "2")
	base.IfName = "vlan100"
	base.IfIndex = 100
	base.BindingVersion = 0
	if _, err := f.a.recordTrafficSample(ctx, observation, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	base.SampleKey = "vlan-second"
	base.ObservedAt = base.ObservedAt.Add(time.Minute)
	base.InOctets = "10001"
	base.OutOctets = "20002"
	if _, err := f.a.recordTrafficSample(ctx, observation, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	detail := trafficGetDetail(t, f, base.ObservedAt)
	if detail["used_bytes"] != "0" {
		t.Fatal("observation billed")
	}
	if _, err := f.a.DB.Exec(ctx, `UPDATE bindings SET active=false,ended_at=now() WHERE id=$1`, f.binding); err != nil {
		t.Fatal(err)
	}
	sample := trafficTestSample(trafficTestTime(), "retired-binding", "1", "2")
	if _, err := f.a.recordTrafficSample(ctx, f.source, sample, &f.admin, true); err == nil {
		t.Fatal("retired binding collected")
	}
	r = testRequest(t, f.a, "DELETE", "/api/v1/traffic/accounts/"+f.account, nil, f.admin)
	if r.Code != 200 {
		t.Fatalf("retire %s", r.Body)
	}
	var active int
	if err := f.a.DB.QueryRow(ctx, `SELECT COUNT(*) FROM traffic_members WHERE active`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("members not released")
	}
	if _, err := f.a.recordTrafficSample(ctx, observation, base, &f.admin, true); err == nil {
		t.Fatal("retired source accepts samples")
	}
}

func TestTrafficPGQualitySequenceAndMockCollector(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	base := trafficTestSample(trafficTestTime(), "baseline", "1000", "2000")
	if _, err := f.a.recordTrafficSample(ctx, f.source, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	offline := base
	offline.SampleKey = "offline"
	offline.ObservedAt = base.ObservedAt.Add(time.Minute)
	offline.InOctets = ""
	offline.OutOctets = ""
	offline.ErrorCode = "mock_offline"
	if _, err := f.a.recordTrafficSample(ctx, f.source, offline, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	restored := base
	restored.SampleKey = "restored"
	restored.ObservedAt = base.ObservedAt.Add(2 * time.Minute)
	restored.InOctets = "1100"
	restored.OutOctets = "2200"
	restored.Uptime = "22000"
	got, err := f.a.recordTrafficSample(ctx, f.source, restored, &f.admin, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["quality"] != "recovered_gap" {
		t.Fatalf("gap lost %v", got)
	}
	remapped := restored
	remapped.SampleKey = "remapped"
	remapped.ObservedAt = restored.ObservedAt.Add(time.Minute)
	remapped.IfIndex = 9
	if _, err = f.a.recordTrafficSample(ctx, f.source, remapped, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	restored.SampleKey = "restored-after-remap"
	restored.ObservedAt = remapped.ObservedAt.Add(time.Minute)
	restored.InOctets = "1500"
	restored.OutOctets = "2600"
	got, err = f.a.recordTrafficSample(ctx, f.source, restored, &f.admin, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["quality"] != "baseline_after_identity_change" {
		t.Fatalf("remap was treated as a gap: %v", got)
	}
	var count int
	if err = f.a.DB.QueryRow(ctx, `SELECT COUNT(*) FROM traffic_ledger`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("remap interval billed")
	}
	late := base
	late.SampleKey = "late"
	late.ObservedAt = base.ObservedAt.Add(30 * time.Second)
	if _, err = f.a.recordTrafficSample(ctx, f.source, late, &f.admin, true); err == nil {
		t.Fatal("late data rewrote ledger")
	}
	r := testRequest(t, f.a, "POST", "/api/v1/traffic/sources/"+f.source+"/collect", map[string]string{"scenario": "reset"}, f.admin)
	trafficResponseID(t, r, 201)
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources/"+f.source+"/collect", map[string]string{"scenario": "normal"}, f.admin)
	trafficResponseID(t, r, 201)
	detail := trafficGetDetail(t, f, time.Now())
	if len(detail["samples"].([]any)) < 7 {
		t.Fatal("mock collector did not persist data")
	}
}

func TestTrafficPGConstraintsSerializeOverlap(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	if _, err := f.a.DB.Exec(ctx, `UPDATE traffic_sources SET active=false,ended_at=now() WHERE id=$1`, f.source); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, kind := range []string{"port", "vlan"} {
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			<-start
			var binding any
			version := 0
			if kind == "port" {
				binding = f.binding
				version = 1
			}
			_, err := f.a.DB.Exec(ctx, `INSERT INTO traffic_sources(id,account_id,device_id,binding_id,binding_version,kind,role,network_domain,vlan_id,if_name,expected_if_index) VALUES($1,$2,$3,$4,$5,$6,'primary','race',333,$6,7)`, newID(), f.account, f.device, binding, version, kind)
			results <- err
		}(kind)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("overlap race success=%d conflicts=%d", success, conflict)
	}
}

func TestTrafficPGSharedPortsAndBoundaryHold(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	binding := newID()
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES($1,$2,$3,'network',8)`, binding, f.server2, f.device); err != nil {
		t.Fatal(err)
	}
	r := testRequest(t, f.a, "POST", "/api/v1/traffic/sources", map[string]any{"account_id": f.account, "binding_id": binding, "kind": "port", "role": "primary", "network_domain": "lab", "vlan_id": 100, "if_name": "mock-eth8", "expected_if_index": 8, "mock": true}, f.admin)
	source2 := trafficResponseID(t, r, 201)
	for i, source := range []string{f.source, source2} {
		base := trafficTestSample(trafficTestTime(), fmt.Sprintf("base%d", i), "100", "200")
		if i == 1 {
			base.IfIndex = 8
			base.IfName = "mock-eth8"
		}
		if _, err := f.a.recordTrafficSample(ctx, source, base, &f.admin, true); err != nil {
			t.Fatal(err)
		}
		base.SampleKey = fmt.Sprintf("second%d", i)
		base.ObservedAt = base.ObservedAt.Add(time.Minute)
		base.InOctets = "300"
		base.OutOctets = "500"
		if _, err := f.a.recordTrafficSample(ctx, source, base, &f.admin, true); err != nil {
			t.Fatal(err)
		}
	}
	detail := trafficGetDetail(t, f, trafficTestTime())
	if detail["used_bytes"] != "1000" {
		t.Fatalf("shared ports did not sum: %v", detail["used_bytes"])
	}
	var alerts int
	if err := f.a.DB.QueryRow(ctx, `SELECT COUNT(*) FROM traffic_alerts WHERE kind='over_quota'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts != 1 {
		t.Fatalf("over quota alert=%d", alerts)
	}
	boundary := trafficNextPeriod(trafficPeriod(trafficTestTime(), TrafficAccount{CycleDay: 1, Timezone: "UTC"}), TrafficAccount{CycleDay: 1, Timezone: "UTC"})
	before := trafficTestSample(boundary.Add(-30*time.Second), "before-boundary", "1000", "2000")
	if _, err := f.a.recordTrafficSample(ctx, f.source, before, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	after := before
	after.SampleKey = "after-boundary"
	after.ObservedAt = boundary.Add(30 * time.Second)
	after.InOctets = "1100"
	after.OutOctets = "2200"
	got, err := f.a.recordTrafficSample(ctx, f.source, after, &f.admin, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["quality"] != "boundary_unallocated" {
		t.Fatalf("boundary %v", got)
	}
	detail = trafficGetDetail(t, f, after.ObservedAt)
	if detail["used_bytes"] != "0" || detail["unallocated_upload_bytes"] != "100" || detail["unallocated_download_bytes"] != "200" {
		t.Fatalf("boundary split invented: %+v", detail)
	}
}

func TestTrafficPGMemberVersionRefreshKeepsHistory(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	sample := trafficTestSample(trafficTestTime(), "first", "1000", "2000")
	if _, err := f.a.recordTrafficSample(ctx, f.source, sample, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	sample.SampleKey = "second"
	sample.ObservedAt = sample.ObservedAt.Add(time.Minute)
	sample.InOctets = "1100"
	sample.OutOctets = "2200"
	if _, err := f.a.recordTrafficSample(ctx, f.source, sample, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.DB.Exec(ctx, `UPDATE servers SET version=version+1 WHERE id=$1`, f.server); err != nil {
		t.Fatal(err)
	}
	r := testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/members/"+f.server+"/refresh", map[string]string{}, f.admin)
	if r.Code != 200 {
		t.Fatalf("refresh %d %s", r.Code, r.Body)
	}
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources", map[string]any{"account_id": f.account, "binding_id": f.binding, "kind": "port", "role": "primary", "network_domain": "lab", "vlan_id": 100, "if_name": "mock-eth7", "expected_if_index": 7, "mock": true}, f.admin)
	replacement := trafficResponseID(t, r, 201)
	sample.SampleKey = "new-baseline"
	sample.ObservedAt = sample.ObservedAt.Add(time.Minute)
	sample.InOctets = "999999"
	sample.OutOctets = "999999"
	got, err := f.a.recordTrafficSample(ctx, replacement, sample, &f.admin, true)
	if err != nil {
		t.Fatal(err)
	}
	if got["quality"] != "baseline" {
		t.Fatal("replacement carried old baseline")
	}
	detail := trafficGetDetail(t, f, sample.ObservedAt)
	if detail["used_bytes"] != "300" {
		t.Fatal("refresh rewrote history")
	}
	if _, err = f.a.DB.Exec(ctx, `UPDATE servers SET owner_id=$2,version=version+1 WHERE id=$1`, f.server, f.other.ID); err != nil {
		t.Fatal(err)
	}
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/members/"+f.server+"/refresh", map[string]string{}, f.admin)
	if r.Code != 409 {
		t.Fatal("refresh accepted changed customer")
	}
}

// The acceptance baseline explicitly names three servers. Keep this fixture exact
// so a two-member aggregation test is not mistaken for A01/A02 evidence.
func trafficThreeServers(t *testing.T) (trafficFixture, []string, []string) {
	t.Helper()
	f := newTrafficFixture(t)
	ctx := context.Background()
	response := testRequest(t, f.a, "DELETE", "/api/v1/traffic/accounts/"+f.account, nil, f.admin)
	if response.Code != 200 {
		t.Fatalf("retire initial fixture: %d %s", response.Code, response.Body)
	}
	servers := []string{f.server, f.server2, newID()}
	bindings := []string{f.binding, newID(), newID()}
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO servers(id,name,owner_id) VALUES($1,$1,$2)`, servers[2], f.owner.ID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		if _, err := f.a.DB.Exec(ctx, `INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES($1,$2,$3,'network',$4)`, bindings[i], servers[i], f.device, 7+i); err != nil {
			t.Fatal(err)
		}
	}
	return f, servers, bindings
}

func trafficThreePortSource(t *testing.T, f trafficFixture, binding string, index int) string {
	t.Helper()
	response := testRequest(t, f.a, "POST", "/api/v1/traffic/sources", map[string]any{"account_id": f.account, "binding_id": binding, "kind": "port", "role": "primary", "network_domain": "three-server-vlan", "vlan_id": 300, "if_name": fmt.Sprintf("mock-eth%d", index), "expected_if_index": index, "mock": true}, f.admin)
	return trafficResponseID(t, response, 201)
}

func trafficThreePortSamples(t *testing.T, f trafficFixture, source string, i int, replay bool) {
	t.Helper()
	ctx := context.Background()
	base := trafficTestSample(trafficTestTime(), "initial", "9007199254740993", "18014398509481986")
	base.IfIndex = 7 + i
	base.IfName = fmt.Sprintf("mock-eth%d", 7+i)
	if _, err := f.a.recordTrafficSample(ctx, source, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	sample := base
	sample.SampleKey = "increment"
	sample.ObservedAt = sample.ObservedAt.Add(time.Minute)
	sample.Uptime = "16000"
	sample.InOctets = fmt.Sprintf("%d", int64(9007199254740993)+int64(i+1)*101)
	sample.OutOctets = fmt.Sprintf("%d", int64(18014398509481986)+int64(i+1)*307)
	recorded, err := f.a.recordTrafficSample(ctx, source, sample, &f.admin, true)
	if err != nil {
		t.Fatal(err)
	}
	if replay {
		for attempt := 0; attempt < 3; attempt++ {
			got, err := f.a.recordTrafficSample(ctx, source, sample, &f.admin, true)
			if err != nil {
				t.Fatal(err)
			}
			if got["duplicate"] != true || got["id"] != recorded["id"] {
				t.Fatalf("replay generated a new sample: %+v", got)
			}
		}
	}
}

func TestTrafficPGThreeIndependentAccountsSameVLAN(t *testing.T) {
	f, servers, bindings := trafficThreeServers(t)
	accounts := make([]string, 3)
	for i := 0; i < 3; i++ {
		response := testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": fmt.Sprintf("Independent server %d", i+1), "owner_id": f.owner.ID, "quota_bytes": "1000000000000", "direction": "both", "timezone": "UTC", "cycle_day": 1, "server_ids": []string{servers[i]}}, f.admin)
		accounts[i] = trafficResponseID(t, response, 201)
		f.account = accounts[i]
		source := trafficThreePortSource(t, f, bindings[i], 7+i)
		trafficThreePortSamples(t, f, source, i, false)
	}
	for i, account := range accounts {
		f.account = account
		detail := trafficGetDetail(t, f, trafficTestTime())
		if detail["upload_bytes"] != fmt.Sprint((i+1)*101) || detail["download_bytes"] != fmt.Sprint((i+1)*307) || detail["used_bytes"] != fmt.Sprint((i+1)*408) {
			t.Fatalf("independent account %d received another port's bytes: %+v", i+1, detail)
		}
		members := detail["members"].([]any)
		if len(members) != 1 || members[0].(map[string]any)["id"] != servers[i] {
			t.Fatalf("independent account %d membership changed: %+v", i+1, members)
		}
	}
	f.account = accounts[0]
	response := testRequest(t, f.a, "POST", "/api/v1/traffic/sources", map[string]any{"account_id": f.account, "device_id": f.device, "kind": "vlan", "role": "primary", "network_domain": "three-server-vlan", "vlan_id": 300, "if_name": "vlan300", "expected_if_index": 300, "mock": true, "coverage_confirmed": true, "vlan_member_ids": []string{servers[0]}}, f.admin)
	if response.Code != 409 {
		t.Fatalf("VLAN primary duplicated three independent port accounts: %d %s", response.Code, response.Body)
	}
	var count int
	if err := f.a.DB.QueryRow(context.Background(), `SELECT count(*) FROM traffic_ledger`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected one interval per independent server, got %d", count)
	}
}

func TestTrafficPGThreeSharedMembersExactReplay(t *testing.T) {
	f, servers, bindings := trafficThreeServers(t)
	response := testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": "Exactly three shared members", "owner_id": f.owner.ID, "quota_bytes": "1000000000000", "direction": "both", "timezone": "UTC", "cycle_day": 1, "server_ids": servers}, f.admin)
	f.account = trafficResponseID(t, response, 201)
	for i := 0; i < 3; i++ {
		source := trafficThreePortSource(t, f, bindings[i], 7+i)
		trafficThreePortSamples(t, f, source, i, true)
	}
	detail := trafficGetDetail(t, f, trafficTestTime())
	if detail["upload_bytes"] != "606" || detail["download_bytes"] != "1842" || detail["used_bytes"] != "2448" || detail["remaining_bytes"] != "999999997552" {
		t.Fatalf("three-member exact aggregate/replay is wrong: %+v", detail)
	}
	members := detail["members"].([]any)
	if len(members) != 3 {
		t.Fatalf("expected exactly three shared members, got %d", len(members))
	}
	expected := map[string]bool{}
	for _, server := range servers {
		expected[server] = true
	}
	for _, raw := range members {
		member := raw.(map[string]any)
		id := member["id"].(string)
		if !expected[id] || member["valid"] != true {
			t.Fatalf("wrong shared membership: %+v", member)
		}
		delete(expected, id)
	}
	for _, server := range servers {
		response = testRequest(t, f.a, "GET", "/api/v1/servers/"+server+"/traffic", nil, f.owner)
		if response.Code != 200 {
			t.Fatalf("shared owner cannot read member traffic: %d %s", response.Code, response.Body)
		}
		var body struct {
			Account TrafficAccount `json:"account"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Account.ID != f.account {
			t.Fatal("shared member resolves a different account")
		}
		response = testRequest(t, f.a, "GET", "/api/v1/servers/"+server+"/traffic", nil, f.other)
		if response.Code != 404 {
			t.Fatalf("other customer accessed shared member: %d", response.Code)
		}
	}
	response = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": "Forbidden cross-customer group", "owner_id": f.other.ID, "quota_bytes": "1000", "direction": "both", "server_ids": servers}, f.admin)
	if response.Code != 409 {
		t.Fatalf("shared members accepted a different account owner: %d %s", response.Code, response.Body)
	}
	var samples, ledger int
	if err := f.a.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM traffic_samples),(SELECT count(*) FROM traffic_ledger)`).Scan(&samples, &ledger); err != nil {
		t.Fatal(err)
	}
	if samples != 6 || ledger != 3 {
		t.Fatalf("repeated three-server collection duplicated accounting: samples=%d ledger=%d", samples, ledger)
	}
}

func TestTrafficPGMemberTransferKeepsSharedPeriod(t *testing.T) {
	f, servers, bindings := trafficThreeServers(t)
	r := testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": "Shared lifecycle", "owner_id": f.owner.ID, "quota_bytes": "1000000000000", "direction": "both", "server_ids": servers}, f.admin)
	f.account = trafficResponseID(t, r, 201)
	sources := make([]string, 3)
	for i := range servers {
		sources[i] = trafficThreePortSource(t, f, bindings[i], 7+i)
		trafficThreePortSamples(t, f, sources[i], i, false)
	}
	at := trafficTestTime()
	period := trafficPeriod(at, TrafficAccount{CycleDay: 1, Timezone: "UTC"})
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+f.account+"/adjustments", map[string]any{"bytes": "500", "reason": "Retain existing quota adjustment", "period_start": period, "idempotency_key": "lifecycle-adjustment"}, f.admin)
	trafficResponseID(t, r, 201)
	ctx := context.Background()
	if _, err := f.a.DB.Exec(ctx, `UPDATE servers SET owner_id=$2,version=version+1 WHERE id=$1`, servers[2], f.other.ID); err != nil {
		t.Fatal(err)
	}
	request := "/api/v1/traffic/accounts/" + f.account + "/members/" + servers[2]
	r = testRequest(t, f.a, "DELETE", request, map[string]string{"reason": "Transferred service leaves shared quota"}, f.owner)
	if r.Code != 403 {
		t.Fatalf("customer changed membership: %d %s", r.Code, r.Body)
	}
	blocked := trafficTestSample(at.Add(2*time.Minute), "blocked-before-exit", "9007199254741194", "18014398509482493")
	blocked.Uptime = "22000"
	if _, err := f.a.recordTrafficSample(ctx, sources[0], blocked, &f.admin, true); err == nil {
		t.Fatal("transfer did not stop old account before explicit removal")
	}
	for i := 0; i < 2; i++ {
		r = testRequest(t, f.a, "DELETE", request, map[string]string{"reason": "Transferred service leaves shared quota"}, f.admin)
		if r.Code != 200 || !strings.Contains(r.Body.String(), fmt.Sprintf(`"changed":%t`, i == 0)) {
			t.Fatalf("exit/replay: %d %s", r.Code, r.Body)
		}
	}
	for i := 0; i < 2; i++ {
		sample := trafficTestSample(at.Add(2*time.Minute), "after-transfer", fmt.Sprint(int64(9007199254740993)+int64(i+1)*201), fmt.Sprint(int64(18014398509481986)+int64(i+1)*507))
		sample.IfIndex, sample.IfName, sample.Uptime = 7+i, fmt.Sprintf("mock-eth%d", 7+i), "22000"
		if _, err := f.a.recordTrafficSample(ctx, sources[i], sample, &f.admin, true); err != nil {
			t.Fatal(err)
		}
	}
	detail := trafficGetDetail(t, f, at)
	if detail["used_bytes"] != "3348" || detail["effective_quota_bytes"] != "1000000000500" || detail["remaining_bytes"] != "999999997152" || len(detail["members"].([]any)) != 2 {
		t.Fatalf("shared balance/history changed: %+v", detail)
	}
	var active bool
	if err := f.a.DB.QueryRow(ctx, `SELECT active FROM traffic_sources WHERE id=$1`, sources[2]).Scan(&active); err != nil || active {
		t.Fatalf("departed source remains active: %v", err)
	}
	var audits int
	if err := f.a.DB.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='traffic.member.remove' AND object_id=$1`, servers[2]).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("exit replay duplicated audit: %d %v", audits, err)
	}
	addPath := "/api/v1/traffic/accounts/" + f.account + "/members"
	for _, body := range []map[string]any{{"server_id": servers[2], "server_version": 2, "reason": "Cannot add another customer"}, {"server_id": servers[0], "server_version": 1, "reason": "Cannot duplicate member"}} {
		r = testRequest(t, f.a, "POST", addPath, body, f.admin)
		if r.Code != 409 {
			t.Fatalf("invalid join accepted: %d %s", r.Code, r.Body)
		}
	}
	if _, err := f.a.DB.Exec(ctx, `UPDATE servers SET owner_id=$2,version=version+1 WHERE id=$1`, servers[2], f.owner.ID); err != nil {
		t.Fatal(err)
	}
	r = testRequest(t, f.a, "POST", addPath, map[string]any{"server_id": servers[2], "server_version": 2, "reason": "Stale join"}, f.admin)
	if r.Code != 409 {
		t.Fatalf("stale join accepted: %d %s", r.Code, r.Body)
	}
	r = testRequest(t, f.a, "POST", addPath, map[string]any{"server_id": servers[2], "server_version": 3, "reason": "Rejoin after ownership confirmation"}, f.admin)
	if r.Code != 201 {
		t.Fatalf("rejoin: %d %s", r.Code, r.Body)
	}
	var history, live int
	if err := f.a.DB.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE active) FROM traffic_members WHERE account_id=$1 AND server_id=$2`, f.account, servers[2]).Scan(&history, &live); err != nil || history != 2 || live != 1 {
		t.Fatalf("membership history lost: %d/%d %v", history, live, err)
	}
	replacement := trafficThreePortSource(t, f, bindings[2], 9)
	sample := trafficTestSample(at.Add(3*time.Minute), "rejoin-baseline", "100", "200")
	sample.IfIndex, sample.IfName = 9, "mock-eth9"
	result, err := f.a.recordTrafficSample(ctx, replacement, sample, &f.admin, true)
	if err != nil || result["quality"] != "baseline" {
		t.Fatalf("rejoin carried previous baseline: %+v %v", result, err)
	}
	if detail := trafficGetDetail(t, f, at); detail["used_bytes"] != "3348" {
		t.Fatalf("rejoin changed old usage: %+v", detail)
	}
}

func TestTrafficPGMemberConcurrentJoinExitAndCollect(t *testing.T) {
	f := newTrafficFixture(t)
	ctx := context.Background()
	path := "/api/v1/traffic/accounts/" + f.account + "/members/" + f.server2
	r := testRequest(t, f.a, "DELETE", path, map[string]string{"reason": "Prepare second account"}, f.admin)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": "Second account", "owner_id": f.owner.ID, "quota_bytes": "1000", "direction": "both", "server_ids": []string{f.server2}}, f.admin)
	secondAccount := trafficResponseID(t, r, 201)
	server := newID()
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO servers(id,name,owner_id) VALUES($1,$1,$2)`, server, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	responses := make(chan *httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	for _, account := range []string{f.account, secondAccount} {
		go func(account string) {
			<-start
			responses <- testRequest(t, f.a, "POST", "/api/v1/traffic/accounts/"+account+"/members", map[string]any{"server_id": server, "server_version": 1, "reason": "Concurrent admission"}, f.admin)
		}(account)
	}
	close(start)
	codes := map[int]int{}
	for i := 0; i < 2; i++ {
		r = <-responses
		codes[r.Code]++
	}
	if codes[201] != 1 || codes[409] != 1 {
		t.Fatalf("concurrent join: %+v", codes)
	}
	var account string
	if err := f.a.DB.QueryRow(ctx, `SELECT account_id FROM traffic_members WHERE server_id=$1 AND active`, server).Scan(&account); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			responses <- testRequest(t, f.a, "DELETE", "/api/v1/traffic/accounts/"+account+"/members/"+server, map[string]string{"reason": "Concurrent exit"}, f.admin)
		}()
	}
	close(start)
	changed := 0
	for i := 0; i < 2; i++ {
		r = <-responses
		if r.Code != 200 {
			t.Fatalf("concurrent exit: %d %s", r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), `"changed":true`) {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("exit committed %d times", changed)
	}
	base := trafficTestSample(trafficTestTime(), "baseline", "10", "20")
	if _, err := f.a.recordTrafficSample(ctx, f.source, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	base.SampleKey, base.ObservedAt, base.InOctets, base.OutOctets = "concurrent", base.ObservedAt.Add(time.Minute), "110", "220"
	errors := make(chan error, 1)
	go func() { _, err := f.a.recordTrafficSample(ctx, f.source, base, &f.admin, true); errors <- err }()
	r = testRequest(t, f.a, "DELETE", "/api/v1/traffic/accounts/"+f.account+"/members/"+f.server, map[string]string{"reason": "Exit during sample"}, f.admin)
	if r.Code != 200 {
		t.Fatalf("collect/exit deadlock or error: %d %s", r.Code, r.Body)
	}
	if err := <-errors; err != nil && !strings.Contains(err.Error(), "retired") {
		t.Fatalf("unexpected concurrent sample error: %v", err)
	}
	var count int
	if err := f.a.DB.QueryRow(ctx, `SELECT count(*) FROM traffic_ledger WHERE source_id=$1`, f.source).Scan(&count); err != nil || count > 1 {
		t.Fatalf("duplicate concurrent ledger: %d %v", count, err)
	}
}

func TestTrafficPGIndependentVLANObservationIsolation(t *testing.T) {
	f, servers, bindings := trafficThreeServers(t)
	accounts := make([]string, 3)
	for i := range servers {
		r := testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": fmt.Sprintf("Observe independent %d", i), "owner_id": f.owner.ID, "quota_bytes": "10000", "direction": "both", "server_ids": []string{servers[i]}}, f.admin)
		accounts[i] = trafficResponseID(t, r, 201)
		f.account = accounts[i]
		source := trafficThreePortSource(t, f, bindings[i], 7+i)
		trafficThreePortSamples(t, f, source, i, false)
	}
	f.account = accounts[0]
	body := map[string]any{"account_id": f.account, "device_id": f.device, "kind": "vlan", "role": "observation", "network_domain": "three-server-vlan", "vlan_id": 300, "if_name": "vlan300", "expected_if_index": 300, "mock": true, "coverage_confirmed": true, "vlan_member_ids": servers}
	r := testRequest(t, f.a, "POST", "/api/v1/traffic/sources", body, f.admin)
	observation := trafficResponseID(t, r, 201)
	ctx := context.Background()
	base := trafficTestSample(trafficTestTime(), "observation-baseline", "1000000", "2000000")
	base.IfIndex, base.IfName, base.BindingVersion = 300, "vlan300", 0
	if _, err := f.a.recordTrafficSample(ctx, observation, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	base.SampleKey, base.ObservedAt, base.InOctets, base.OutOctets, base.Uptime = "observation-delta", base.ObservedAt.Add(time.Minute), "1007000", "2009000", "16000"
	if _, err := f.a.recordTrafficSample(ctx, observation, base, &f.admin, true); err != nil {
		t.Fatal(err)
	}
	detail := trafficGetDetail(t, f, trafficTestTime())
	if detail["used_bytes"] != "408" {
		t.Fatalf("VLAN observation billed independent account: %+v", detail)
	}
	var billable bool
	if err := f.a.DB.QueryRow(ctx, `SELECT billable FROM traffic_ledger WHERE source_id=$1`, observation).Scan(&billable); err != nil || billable {
		t.Fatalf("observation was billed: %v", err)
	}
	encoded, _ := json.Marshal(detail)
	if strings.Contains(string(encoded), servers[1]) || strings.Contains(string(encoded), servers[2]) {
		t.Fatal("customer source exposed unrelated account server identifiers")
	}
	body["vlan_member_ids"] = servers[:2]
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources", body, f.admin)
	if r.Code != 409 {
		t.Fatalf("incomplete VLAN coverage accepted: %d %s", r.Code, r.Body)
	}
	// An existing observation must not silently admit a newly declared port.
	server, binding := newID(), newID()
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO servers(id,name,owner_id) VALUES($1,$1,$2)`, server, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.DB.Exec(ctx, `INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES($1,$2,$3,'network',10)`, binding, server, f.device); err != nil {
		t.Fatal(err)
	}
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/accounts", map[string]any{"name": "Uncovered server", "owner_id": f.owner.ID, "quota_bytes": "1000", "direction": "both", "server_ids": []string{server}}, f.admin)
	fourth := trafficResponseID(t, r, 201)
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources", map[string]any{"account_id": fourth, "binding_id": binding, "kind": "port", "role": "primary", "network_domain": "three-server-vlan", "vlan_id": 300, "if_name": "mock-eth10", "expected_if_index": 10, "mock": true}, f.admin)
	if r.Code != 409 {
		t.Fatalf("unconfirmed port silently expanded observation: %d %s", r.Code, r.Body)
	}
	if _, err := f.a.DB.Exec(ctx, `UPDATE servers SET owner_id=$2,version=version+1 WHERE id=$1`, servers[2], f.other.ID); err != nil {
		t.Fatal(err)
	}
	base.SampleKey, base.ObservedAt = "after-customer-transfer", base.ObservedAt.Add(time.Minute)
	if _, err := f.a.recordTrafficSample(ctx, observation, base, &f.admin, true); err == nil {
		t.Fatal("observation kept collecting another customer's traffic")
	}
	detail = trafficGetDetail(t, f, trafficTestTime())
	encoded, _ = json.Marshal(detail)
	if strings.Contains(string(encoded), observation) || detail["used_bytes"] != "408" {
		t.Fatalf("unsafe observation remains visible or changes billing: %s", encoded)
	}
	r = testRequest(t, f.a, "GET", "/api/v1/servers/"+servers[0]+"/traffic?at="+trafficTestTime().Format(time.RFC3339), nil, f.owner)
	if r.Code != 200 || strings.Contains(r.Body.String(), observation) || strings.Contains(r.Body.String(), servers[2]) {
		t.Fatalf("server/WHMCS traffic path leaks transferred coverage: %d %s", r.Code, r.Body)
	}
	body["vlan_member_ids"] = servers
	r = testRequest(t, f.a, "POST", "/api/v1/traffic/sources", body, f.admin)
	if r.Code != 409 {
		t.Fatalf("cross-customer VLAN coverage accepted: %d %s", r.Code, r.Body)
	}
	r = testRequest(t, f.a, "GET", "/api/v1/traffic/accounts/"+f.account, nil, f.other)
	if r.Code != 404 {
		t.Fatalf("other customer read observation account: %d", r.Code)
	}
	r = testRequest(t, f.a, "GET", "/api/v1/traffic/accounts/"+f.account, nil, f.admin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), observation) {
		t.Fatal("admin lost diagnostic source history")
	}
	r = testRequest(t, f.a, "DELETE", "/api/v1/traffic/accounts/"+accounts[2]+"/members/"+servers[2], map[string]string{"reason": "End old customer's membership"}, f.admin)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var active bool
	if err := f.a.DB.QueryRow(ctx, `SELECT active FROM traffic_sources WHERE id=$1`, observation).Scan(&active); err != nil || active {
		t.Fatalf("cross-account observation not retired on member exit: %v", err)
	}
}

func TestTrafficPGLegacyMembershipMigrationPreservesData(t *testing.T) {
	baseApp := testApp(t)
	ctx := context.Background()
	schema := "traffic_upgrade_" + newID()
	if _, err := baseApp.DB.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	conf := baseApp.DB.Config().Copy()
	conf.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, conf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = baseApp.DB.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	if _, err = db.Exec(ctx, `CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_base.sql", "002_traffic.sql", "003_console.sql", "004_whmcs.sql", "005_binding_guards.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(data)); err != nil {
			t.Fatalf("legacy %s: %v", name, err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	a := New(db, Config{BaseURL: "http://localhost"})
	for _, sql := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('owner','legacy@example.test','unused','customer')`,
		`INSERT INTO servers(id,name,owner_id) VALUES('server','legacy server','owner')`,
		`INSERT INTO devices(id,name,kind) VALUES('device','legacy device','mock_bmc')`,
		`INSERT INTO bindings(id,server_id,device_id,kind,port) VALUES('binding','server','device','network',7)`,
		`INSERT INTO traffic_accounts(id,name,owner_id,quota_bytes,direction) VALUES('account','legacy account','owner',1000,'both')`,
		`INSERT INTO traffic_members(account_id,server_id,server_version) VALUES('account','server',1)`,
		`INSERT INTO traffic_sources(id,account_id,device_id,binding_id,binding_version,kind,role,network_domain,vlan_id,if_name,expected_if_index) VALUES('port','account','device','binding',1,'port','primary','legacy',100,'mock-eth7',7)`,
		`INSERT INTO traffic_sources(id,account_id,device_id,kind,role,network_domain,vlan_id,if_name,expected_if_index) VALUES('vlan','account','device','vlan','observation','legacy',100,'vlan100',100)`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	sample := trafficTestSample(trafficTestTime(), "legacy-baseline", "9007199254740993", "1000")
	if _, err = a.recordTrafficSample(ctx, "port", sample, nil, true); err != nil {
		t.Fatal(err)
	}
	sample.SampleKey, sample.ObservedAt, sample.InOctets, sample.OutOctets = "legacy-increment", sample.ObservedAt.Add(time.Minute), "9007199254741094", "1307"
	if _, err = a.recordTrafficSample(ctx, "port", sample, nil, true); err != nil {
		t.Fatal(err)
	}
	fingerprint := func() string {
		var data string
		err := db.QueryRow(ctx, `SELECT jsonb_build_object('accounts',(SELECT jsonb_agg(to_jsonb(a)) FROM traffic_accounts a),'members',(SELECT jsonb_agg(to_jsonb(m)-'id') FROM traffic_members m),'samples',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM traffic_samples s),'ledger',(SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM traffic_ledger l),'sources',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM traffic_sources s))::text`).Scan(&data)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := fingerprint()
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if after := fingerprint(); after != before {
		t.Fatalf("006 changed legacy membership/account/source/sample/ledger data\nbefore %s\nafter %s", before, after)
	}
	var version int64
	if err = db.QueryRow(ctx, `SELECT server_version FROM traffic_source_members WHERE source_id='vlan' AND server_id='server'`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("legacy VLAN coverage not migrated: %d %v", version, err)
	}
	sample.SampleKey, sample.ObservedAt, sample.InOctets, sample.OutOctets = "after-upgrade", sample.ObservedAt.Add(time.Minute), "9007199254741195", "1614"
	if _, err = a.recordTrafficSample(ctx, "port", sample, nil, true); err != nil {
		t.Fatal(err)
	}
	var used string
	if err = db.QueryRow(ctx, `SELECT sum(upload_bytes+download_bytes)::text FROM traffic_ledger WHERE billable`).Scan(&used); err != nil || used != "816" {
		t.Fatalf("upgrade broke continuous accounting: %s %v", used, err)
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
}
