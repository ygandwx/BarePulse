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

// ParseSNMPv3Algorithms requires exact, explicit profile names. AES192/AES256
// retain their existing Blumenthal meaning; the C variants use Reeder extension.
// SHA (SHA-1) is retained for explicitly enrolled legacy equipment. MD5 and DES
// are deliberately not offered, and nothing falls back to a weaker algorithm.
func ParseSNMPv3Algorithms(authName, privacyName string) (gosnmp.SnmpV3AuthProtocol, gosnmp.SnmpV3PrivProtocol, error) {
	auth, authOK := map[string]gosnmp.SnmpV3AuthProtocol{"SHA": gosnmp.SHA, "SHA224": gosnmp.SHA224, "SHA256": gosnmp.SHA256, "SHA384": gosnmp.SHA384, "SHA512": gosnmp.SHA512}[authName]
	privacy, privacyOK := map[string]gosnmp.SnmpV3PrivProtocol{"AES": gosnmp.AES, "AES192": gosnmp.AES192, "AES256": gosnmp.AES256, "AES192C": gosnmp.AES192C, "AES256C": gosnmp.AES256C}[privacyName]
	if !authOK || !privacyOK {
		return 0, 0, errors.New("explicit supported SNMPv3 authentication and privacy algorithms required")
	}
	return auth, privacy, nil
}

// ValidateSNMPv3Security also protects direct enum callers: gosnmp's validation
// accepts some out-of-range values which must never reach its crypto code.
func ValidateSNMPv3Security(username, authPassword, privacyPassword string, auth gosnmp.SnmpV3AuthProtocol, privacy gosnmp.SnmpV3PrivProtocol) error {
	if _, _, err := ParseSNMPv3Algorithms(auth.String(), privacy.String()); err != nil {
		return err
	}
	if len(username) < 1 || len(username) > 32 || len(authPassword) < 8 || len(authPassword) > 1024 || len(privacyPassword) < 8 || len(privacyPassword) > 1024 || strings.ContainsAny(username+authPassword+privacyPassword, "\x00\r\n") {
		return errors.New("bounded SNMPv3 AuthPriv credentials required")
	}
	return nil
}

// ValidateSNMPv3Response applies to the final data response, not the initial
// unauthenticated engine discovery REPORT. Authentication alone is insufficient
// when a peer replies in cleartext to an AuthPriv request.
func ValidateSNMPv3Response(packet *gosnmp.SnmpPacket) error {
	if packet == nil || packet.Version != gosnmp.Version3 || packet.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv || packet.SecurityModel != gosnmp.UserSecurityModel || packet.PDUType != gosnmp.GetResponse {
		return errors.New("SNMPv3 authenticated encrypted response required")
	}
	return nil
}

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
	if err := ValidateSNMPv3Security(s.Username, s.AuthPassword, s.PrivacyPassword, s.AuthProtocol, s.PrivacyProtocol); err != nil {
		return nil, err
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
	if err := ValidateSNMPv3Response(p); err != nil {
		return 0, err
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
	if err := ValidateSNMPv3Response(preflight); err != nil {
		return err
	}
	if _, err := integerPacket(preflight, s.DiscoveryOID); err != nil {
		return err
	}
	client.Conn = &oneWriteConn{Conn: client.Conn}
	p, err := client.Set([]gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.Integer, Value: value}})
	if err != nil {
		return errors.New("SNMP write outcome unknown")
	}
	if err := ValidateSNMPv3Response(p); err != nil {
		return errors.New("SNMP write outcome unknown")
	}
	got, err := integerPacket(p, oid)
	if err != nil || got != value {
		return errors.New("SNMP SET acknowledgement invalid")
	}
	return nil
}
