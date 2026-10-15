package drivers

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// SNMPV3Transport uses AuthPriv with explicit algorithms; it never falls back to
// a community string. DiscoveryOID must be a confirmed, read-only profile OID.
type SNMPV3Transport struct {
	AllowReal, AllowLoopback, AllowWrites   bool
	AllowedTargets                          []string
	Username, AuthPassword, PrivacyPassword string
	AuthProtocol                            gosnmp.SnmpV3AuthProtocol
	PrivacyProtocol                         gosnmp.SnmpV3PrivProtocol
	DiscoveryOID                            string
	Timeout                                 time.Duration
}

func (s *SNMPV3Transport) session(ctx context.Context, t Target, write bool) (*gosnmp.GoSNMP, error) {
	host, portString, err := net.SplitHostPort(t.Address)
	if err != nil {
		host = t.Address
		portString = "161"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, errors.New("SNMP target must be an enrolled literal IP")
	}
	allowed := ip.IsLoopback() && s.AllowLoopback
	if !allowed && s.AllowReal {
		for _, target := range s.AllowedTargets {
			if t.Address == target {
				allowed = true
			}
		}
	}
	if !allowed || (write && !s.AllowWrites) {
		return nil, errors.New("SNMP access disabled")
	}
	port, err := strconv.ParseUint(portString, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("invalid SNMP port")
	}
	if s.Username == "" || s.AuthPassword == "" || s.PrivacyPassword == "" || s.AuthProtocol == gosnmp.NoAuth || s.PrivacyProtocol == gosnmp.NoPriv {
		return nil, errors.New("explicit SNMPv3 AuthPriv credentials and algorithms required")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := &gosnmp.GoSNMP{Target: host, Port: uint16(port), Transport: "udp", Version: gosnmp.Version3, Context: ctx, Timeout: timeout, Retries: 0, MaxOids: 1, MsgFlags: gosnmp.AuthPriv, SecurityModel: gosnmp.UserSecurityModel, SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: s.Username, AuthenticationProtocol: s.AuthProtocol, AuthenticationPassphrase: s.AuthPassword, PrivacyProtocol: s.PrivacyProtocol, PrivacyPassphrase: s.PrivacyPassword}}
	if err := client.Connect(); err != nil {
		return nil, errors.New("SNMP connection failed")
	}
	return client, nil
}

func integerPacket(p *gosnmp.SnmpPacket, oid string) (int, error) {
	if p == nil || p.Error != gosnmp.NoError || len(p.Variables) != 1 {
		return 0, errors.New("SNMP response error")
	}
	v := p.Variables[0]
	if v.Type != gosnmp.Integer || strings.TrimPrefix(v.Name, ".") != strings.TrimPrefix(oid, ".") {
		return 0, errors.New("SNMP response OID or type mismatch")
	}
	i, ok := v.Value.(int)
	if !ok {
		return 0, errors.New("SNMP integer value missing")
	}
	return i, nil
}

func (s *SNMPV3Transport) GetInteger(ctx context.Context, t Target, oid string) (int, error) {
	if !validOID(oid) {
		return 0, errors.New("invalid profile OID")
	}
	client, err := s.session(ctx, t, false)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	p, err := client.Get([]string{oid})
	if err != nil {
		return 0, errors.New("SNMP read failed")
	}
	return integerPacket(p, oid)
}

// oneWriteConn is a second barrier beyond Retries:0: gosnmp may internally
// resynchronize SNMPv3 REPORTs. After discovery, allow only one datagram write,
// so no library recovery path can resend a non-idempotent SET.
type oneWriteConn struct {
	net.Conn
	written bool
}

func (c *oneWriteConn) Write(b []byte) (int, error) {
	if c.written {
		return 0, errors.New("SNMP SET resend blocked")
	}
	c.written = true
	return c.Conn.Write(b)
}

func (s *SNMPV3Transport) SetInteger(ctx context.Context, t Target, oid string, value int) error {
	if !validOID(oid) || !validOID(s.DiscoveryOID) {
		return errors.New("confirmed command and discovery OIDs required")
	}
	client, err := s.session(ctx, t, true)
	if err != nil {
		return err
	}
	defer client.Close()
	// Discover/time-synchronize using a harmless read before the sole SET.
	preflight, err := client.Get([]string{s.DiscoveryOID})
	if err != nil {
		return errors.New("SNMP preflight failed")
	}
	if _, err := integerPacket(preflight, s.DiscoveryOID); err != nil {
		return err
	}
	client.Conn = &oneWriteConn{Conn: client.Conn}
	p, err := client.Set([]gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.Integer, Value: value}})
	if err != nil {
		return errors.New("SNMP write outcome unknown")
	}
	got, err := integerPacket(p, oid)
	if err != nil || got != value {
		return errors.New("SNMP SET acknowledgement invalid")
	}
	return nil
}
