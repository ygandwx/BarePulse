package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/jackc/pgx/v5"
	"github.com/ygandwx/BarePulse/backend/internal/drivers"
)

// TrafficCollector is read-only. Implementations return cumulative counters rather
// than billed bytes; all attribution and accounting remains in recordTrafficSample.
type TrafficCollector interface {
	Collect(context.Context, TrafficSource) (TrafficSample, error)
}

type SNMPTrafficCredentials struct {
	Username        string `json:"username"`
	AuthProtocol    string `json:"auth_protocol"`
	PrivacyProtocol string `json:"privacy_protocol"`
	AuthPassword    string `json:"auth_password"`
	PrivacyPassword string `json:"privacy_password"`
	ContextName     string `json:"context_name"`
}

type SNMPTrafficCollector struct {
	Allowed     bool
	Address     string
	Credentials SNMPTrafficCredentials
}

// RFC 2863 IF-MIB and RFC 3418 sysUpTime. These are standard read-only objects,
// not a claim that a particular SN2410/Onyx or VyOS firmware implements them.
const trafficIfNameOID = ".1.3.6.1.2.1.31.1.1.1.1"
const trafficUptimeOID = ".1.3.6.1.2.1.1.3.0"

func trafficOIDs(index int) []string {
	suffix := "." + strconv.Itoa(index)
	return []string{trafficUptimeOID, trafficIfNameOID + suffix, ".1.3.6.1.2.1.31.1.1.1.6" + suffix, ".1.3.6.1.2.1.31.1.1.1.10" + suffix, ".1.3.6.1.2.1.31.1.1.1.19" + suffix, ".1.3.6.1.2.1.31.1.1.1.18" + suffix, ".1.3.6.1.2.1.2.2.1.8" + suffix}
}

func (c SNMPTrafficCollector) client(ctx context.Context) (*gosnmp.GoSNMP, error) {
	if !c.Allowed {
		return nil, fail(409, "real_devices_disabled", "Real device access is disabled")
	}
	auth, privacy, err := drivers.ParseSNMPv3Algorithms(c.Credentials.AuthProtocol, c.Credentials.PrivacyProtocol)
	if err != nil || drivers.ValidateSNMPv3Security(c.Credentials.Username, c.Credentials.AuthPassword, c.Credentials.PrivacyPassword, auth, privacy) != nil {
		return nil, fail(409, "snmp_credentials_invalid", "Explicit supported SNMPv3 AuthPriv algorithms and bounded credentials are required")
	}
	host := c.Address
	port := uint64(161)
	if parsed, p, err := net.SplitHostPort(c.Address); err == nil {
		host = parsed
		port, err = strconv.ParseUint(p, 10, 16)
		if err != nil || port == 0 {
			return nil, fail(400, "invalid_snmp_address", "Invalid SNMP port")
		}
	}
	if host == "" || strings.ContainsAny(host, "/\\@?# \r\n\t") {
		return nil, fail(400, "invalid_snmp_address", "Use a registered hostname or IP, optionally with port")
	}
	client := &gosnmp.GoSNMP{Target: host, Port: uint16(port), Transport: "udp", Version: gosnmp.Version3, Context: ctx, Timeout: 3 * time.Second, Retries: 0, MaxOids: 20, MaxRepetitions: 20, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, ContextName: c.Credentials.ContextName, SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: c.Credentials.Username, AuthenticationProtocol: auth, AuthenticationPassphrase: c.Credentials.AuthPassword, PrivacyProtocol: privacy, PrivacyPassphrase: c.Credentials.PrivacyPassword}}
	if err := client.Connect(); err != nil {
		return nil, errors.New("SNMP connection failed")
	}
	return client, nil
}

func (c SNMPTrafficCollector) Collect(ctx context.Context, src TrafficSource) (TrafficSample, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var sample TrafficSample
	client, err := c.client(ctx)
	if err != nil {
		return sample, err
	}
	defer client.Conn.Close()
	packet, err := client.Get(trafficOIDs(src.ExpectedIfIndex))
	if err != nil {
		return sample, sanitizedSNMPError(ctx, "SNMP collection failed")
	}
	if err := drivers.ValidateSNMPv3Response(packet); err != nil {
		return sample, err
	}
	return trafficParseSNMP(packet, src, time.Now().UTC())
}

func sanitizedSNMPError(ctx context.Context, message string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New(message)
}

type TrafficInterface struct {
	IfIndex int    `json:"if_index"`
	IfName  string `json:"if_name"`
}

// Discover offers stable names for an explicitly authorized onboarding workflow.
// It does not silently change an existing source's pinned ifIndex or binding.
func (c SNMPTrafficCollector) Discover(ctx context.Context) ([]TrafficInterface, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Conn.Close()
	interfaces := []TrafficInterface{}
	// Inspect every response envelope; BulkWalk hides AuthPriv flags from its
	// callback and would permit an authenticated but cleartext final response.
	cursor, lastIndex := trafficIfNameOID, 0
	for {
		packet, err := client.GetBulk([]string{cursor}, 0, 20)
		if err != nil {
			return nil, sanitizedSNMPError(ctx, "SNMP interface discovery failed")
		}
		if err := drivers.ValidateSNMPv3Response(packet); err != nil {
			return nil, err
		}
		if packet.Error != gosnmp.NoError || len(packet.Variables) == 0 {
			return nil, errors.New("SNMP interface discovery response error")
		}
		for _, pdu := range packet.Variables {
			prefix := trafficIfNameOID + "."
			name := "." + strings.TrimPrefix(pdu.Name, ".")
			if pdu.Type == gosnmp.NoSuchObject || pdu.Type == gosnmp.NoSuchInstance {
				return nil, errors.New("SNMP interface discovery object unavailable")
			}
			if pdu.Type == gosnmp.EndOfMibView || !strings.HasPrefix(name, prefix) {
				return interfaces, nil
			}
			index, e := strconv.Atoi(strings.TrimPrefix(name, prefix))
			if e != nil || index <= lastIndex {
				return nil, errors.New("invalid or non-increasing interface index")
			}
			v, ok := pdu.Value.([]byte)
			if !ok || pdu.Type != gosnmp.OctetString {
				return nil, errors.New("invalid interface name")
			}
			if len(interfaces) >= 10000 {
				return nil, errors.New("interface discovery exceeded bound")
			}
			interfaces = append(interfaces, TrafficInterface{IfIndex: index, IfName: string(v)})
			cursor, lastIndex = name, index
		}
	}
}

func trafficParseSNMP(packet *gosnmp.SnmpPacket, src TrafficSource, at time.Time) (TrafficSample, error) {
	sample := TrafficSample{SampleKey: newID(), ObservedAt: at, IfIndex: src.ExpectedIfIndex, BindingVersion: src.BindingVersion, LinkState: "unknown"}
	if packet == nil || packet.Error != gosnmp.NoError {
		return sample, errors.New("SNMP packet error")
	}
	oids := trafficOIDs(src.ExpectedIfIndex)
	values := map[string]gosnmp.SnmpPDU{}
	for _, p := range packet.Variables {
		key := p.Name
		if !strings.HasPrefix(key, ".") {
			key = "." + key
		}
		if _, exists := values[key]; exists {
			return sample, errors.New("duplicate SNMP object")
		}
		values[key] = p
	}
	for _, oid := range oids {
		if _, ok := values[oid]; !ok {
			return sample, errors.New("incomplete SNMP counter response")
		}
	}
	number := func(oid string, want gosnmp.Asn1BER) (string, error) {
		p := values[oid]
		if p.Type != want {
			return "", errors.New("SNMP counter type mismatch")
		}
		switch p.Value.(type) {
		case int, int32, int64, uint, uint32, uint64:
		default:
			return "", errors.New("SNMP counter value type mismatch")
		}
		n := gosnmp.ToBigInt(p.Value)
		if n.Sign() < 0 || n.BitLen() > 64 {
			return "", errors.New("SNMP counter out of range")
		}
		return n.String(), nil
	}
	text := func(oid string) (string, error) {
		p := values[oid]
		v, ok := p.Value.([]byte)
		if !ok || p.Type != gosnmp.OctetString {
			return "", errors.New("SNMP text type mismatch")
		}
		return string(v), nil
	}
	var err error
	if sample.Uptime, err = number(oids[0], gosnmp.TimeTicks); err != nil {
		return sample, err
	}
	if sample.IfName, err = text(oids[1]); err != nil {
		return sample, err
	}
	if sample.InOctets, err = number(oids[2], gosnmp.Counter64); err != nil {
		return sample, err
	}
	if sample.OutOctets, err = number(oids[3], gosnmp.Counter64); err != nil {
		return sample, err
	}
	if sample.Discontinuity, err = number(oids[4], gosnmp.TimeTicks); err != nil {
		return sample, err
	}
	if sample.IfAlias, err = text(oids[5]); err != nil {
		return sample, err
	}
	state, err := number(oids[6], gosnmp.Integer)
	if err != nil {
		return sample, err
	}
	switch state {
	case "1":
		sample.LinkState = "up"
	case "2":
		sample.LinkState = "down"
	}
	return sample, nil
}

func (a *App) collectTrafficSource(w http.ResponseWriter, r *http.Request, u User) error {
	var body struct {
		Scenario string `json:"scenario"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Scenario == "" {
		body.Scenario = "normal"
	}
	result, err := a.pollTrafficSource(r.Context(), r.PathValue("id"), body.Scenario, &u, false)
	if err != nil {
		return err
	}
	jsonResponse(w, 201, result)
	return nil
}

func (a *App) pollTrafficSource(ctx context.Context, id, scenario string, actor *User, scheduled bool) (map[string]any, error) {
	// A session-level advisory lock prevents two workers from querying the same source
	// concurrently. It is local scheduling only; no device-control operations exist here.
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	var held bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,1481))`, id).Scan(&held); err != nil {
		return nil, err
	}
	if !held {
		return nil, fail(409, "collector_busy", "Source is already being collected")
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,1481))`, id)
	}()
	src, err := trafficScanSource(conn.QueryRow(ctx, `SELECT `+trafficSourceCols+` FROM traffic_sources WHERE id=$1`, id))
	if err != nil {
		return nil, dbError(err)
	}
	if !src.Active {
		return nil, fail(409, "source_retired", "Source is retired")
	}
	if scheduled {
		var due bool
		if err = conn.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM traffic_samples WHERE source_id=$1 AND observed_at>now()-make_interval(secs=>$2))`, id, src.IntervalSeconds).Scan(&due); err != nil {
			return nil, err
		}
		if !due {
			return map[string]any{"skipped": true}, nil
		}
	}
	var sample TrafficSample
	if src.Mock {
		prev, e := trafficScanSample(conn.QueryRow(ctx, `SELECT `+trafficSampleCols+` FROM traffic_samples WHERE source_id=$1 AND error_code='' ORDER BY observed_at DESC LIMIT 1`, id))
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		sample, err = mockTrafficSample(src, prev, scenario, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	} else {
		if scenario != "normal" {
			return nil, fail(400, "mock_scenario_only", "Fault scenarios are only available for mock sources")
		}
		if !a.Config.AllowRealDevices {
			return nil, fail(409, "real_devices_disabled", "Real SNMP is disabled; mock sources remain available")
		}
		var address, credential, kind string
		var enabled bool
		if err = conn.QueryRow(ctx, `SELECT address,credential_ref,kind,enabled FROM devices WHERE id=$1`, src.DeviceID).Scan(&address, &credential, &kind, &enabled); err != nil {
			return nil, err
		}
		if !enabled || kind != "snmp" {
			return nil, fail(409, "invalid_device", "Registered SNMP device is disabled or changed")
		}
		var creds SNMPTrafficCredentials
		if credential == "" || json.Unmarshal([]byte(os.Getenv(credential)), &creds) != nil {
			return nil, fail(409, "snmp_credentials_missing", "SNMP credential environment reference is missing or invalid")
		}
		collector := SNMPTrafficCollector{Allowed: a.Config.AllowRealDevices, Address: address, Credentials: creds}
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		sample, err = collector.Collect(pollCtx, src)
		cancel()
		if err != nil {
			sample = TrafficSample{SampleKey: newID(), ObservedAt: time.Now().UTC(), IfIndex: src.ExpectedIfIndex, IfName: src.IfName, Uptime: "0", Discontinuity: "0", LinkState: "unknown", BindingVersion: src.BindingVersion, ErrorCode: trafficFailureCode(err)}
		}
	}
	return a.recordTrafficSample(ctx, id, sample, actor, false)
}

func mockTrafficSample(src TrafficSource, prev TrafficSample, scenario string, at time.Time) (TrafficSample, error) {
	sample := TrafficSample{SampleKey: newID(), ObservedAt: at, IfName: src.IfName, IfIndex: src.ExpectedIfIndex, IfAlias: "BarePulse simulated traffic", BindingVersion: src.BindingVersion, Uptime: "10000", Discontinuity: "0", InOctets: "9007199254740993", OutOctets: "18014398509481986", LinkState: "up"}
	if prev.ID != "" {
		elapsed := at.Sub(prev.ObservedAt)
		if elapsed <= 0 {
			return sample, fail(409, "clock_not_advanced", "Collector clock must advance")
		}
		increment := func(base string, rate int64) string {
			n, ok := new(big.Int).SetString(base, 10)
			if !ok {
				n = new(big.Int)
			}
			delta := new(big.Int).Mul(big.NewInt(elapsed.Nanoseconds()), big.NewInt(rate))
			delta.Quo(delta, big.NewInt(int64(time.Second)))
			return n.Add(n, delta).String()
		}
		sample.InOctets = increment(prev.InOctets, 125000)
		sample.OutOctets = increment(prev.OutOctets, 250000)
		sample.Uptime = increment(prev.Uptime, 100)
		sample.Discontinuity = prev.Discontinuity
	}
	switch scenario {
	case "normal":
	case "offline":
		sample.InOctets = ""
		sample.OutOctets = ""
		sample.ErrorCode = "mock_offline"
		sample.LinkState = "unknown"
	case "reset":
		sample.InOctets = "10"
		sample.OutOctets = "20"
	case "reboot":
		sample.InOctets = "10"
		sample.OutOctets = "20"
		sample.Uptime = "1"
	case "discontinuity":
		sample.Discontinuity = sample.Uptime
	case "remap":
		sample.IfIndex++
	default:
		return sample, fail(400, "invalid_scenario", "Select normal, offline, reset, reboot, discontinuity or remap")
	}
	return sample, nil
}

func (a *App) RunTrafficCollector(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		rows, err := a.DB.Query(ctx, `SELECT s.id FROM traffic_sources s JOIN devices d ON d.id=s.device_id WHERE s.active AND s.auto_collect AND d.enabled AND (s.mock OR $1) AND NOT EXISTS(SELECT 1 FROM traffic_samples p WHERE p.source_id=s.id AND p.observed_at>now()-make_interval(secs=>s.interval_seconds)) ORDER BY s.id LIMIT 100`, a.Config.AllowRealDevices)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("traffic scheduler query failed")
			}
			continue
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			continue
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, e := a.pollTrafficSource(pollCtx, id, "normal", nil, true)
			cancel()
			if e != nil {
				slog.Warn("traffic source collection blocked", "source_id", id, "error_type", fmt.Sprintf("%T", e))
			}
		}
	}
}
