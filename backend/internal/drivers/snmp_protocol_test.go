package drivers

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"encoding/asn1"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

const snmpFixtureEngine = "\x80\x00\x00\x00\x05offline-fixture"
const snmpFixtureStateOID = ".1.3.6.1.4.1.99999.1.0"
const snmpFixtureControlOID = ".1.3.6.1.4.1.99999.2.0"
const snmpFixtureAuth = "offline-auth-passphrase"
const snmpFixturePrivacy = "offline-privacy-passphrase"

type snmpWirePeer struct {
	conn                       *net.UDPConn
	security                   *gosnmp.UsmSecurityParameters
	mode                       string
	mu                         sync.Mutex
	discoveries, reads, writes int
	done                       chan struct{}
}

// This fixture exchanges actual UDP SNMPv3 discovery/USM/BER packets. It uses
// gosnmp's packet codec, plus independent standard-library HMAC verification.
// It is not evidence that any vendor agent implements these algorithms/OIDs.
func newSNMPWirePeer(t *testing.T, auth gosnmp.SnmpV3AuthProtocol, privacy gosnmp.SnmpV3PrivProtocol, mode string) (*snmpWirePeer, *SNMPV3Transport, Target) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	p := &snmpWirePeer{conn: conn, mode: mode, done: make(chan struct{}), security: &gosnmp.UsmSecurityParameters{
		UserName: "offline-user", AuthenticationProtocol: auth, AuthenticationPassphrase: snmpFixtureAuth,
		PrivacyProtocol: privacy, PrivacyPassphrase: snmpFixturePrivacy,
		AuthoritativeEngineID: snmpFixtureEngine, AuthoritativeEngineBoots: 3, AuthoritativeEngineTime: 100,
	}}
	if err := p.security.InitSecurityKeys(); err != nil {
		t.Fatal(err)
	}
	go p.serve(t)
	t.Cleanup(func() { _ = conn.Close(); <-p.done })
	transport := &SNMPV3Transport{AllowLoopback: true, AllowWrites: true, Username: p.security.UserName,
		AuthPassword: snmpFixtureAuth, PrivacyPassword: snmpFixturePrivacy, AuthProtocol: auth, PrivacyProtocol: privacy,
		DiscoveryOID: snmpFixtureStateOID, Timeout: 250 * time.Millisecond}
	return p, transport, Target{DeviceID: "offline-snmp", Kind: "apc", Address: conn.LocalAddr().String(), Port: 1}
}

func (p *snmpWirePeer) counts() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discoveries, p.reads, p.writes
}

func (p *snmpWirePeer) serve(t *testing.T) {
	defer close(p.done)
	decoder := &gosnmp.GoSNMP{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, SecurityParameters: p.security}
	buffer := make([]byte, 65535)
	for {
		n, addr, err := p.conn.ReadFromUDP(buffer)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Errorf("fixture read: %v", err)
			}
			return
		}
		wire := append([]byte(nil), buffer[:n]...)
		// Decode a copy: authentication verification can zero the digest field.
		request, err := decoder.UnmarshalTrap(append([]byte(nil), wire...), true)
		if err != nil {
			t.Errorf("fixture could not authenticate/decrypt request: %v", err)
			continue
		}
		usm := request.SecurityParameters.(*gosnmp.UsmSecurityParameters)
		response := &gosnmp.SnmpPacket{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv,
			PDUType: gosnmp.GetResponse, MsgID: request.MsgID, RequestID: request.RequestID, MsgMaxSize: 65507,
			ContextEngineID: snmpFixtureEngine, ContextName: request.ContextName, SecurityParameters: p.security.Copy()}
		if usm.UserName == "" && request.MsgFlags&gosnmp.AuthPriv == gosnmp.NoAuthNoPriv {
			p.mu.Lock()
			p.discoveries++
			p.mu.Unlock()
			response.MsgFlags = gosnmp.NoAuthNoPriv
			response.PDUType = gosnmp.Report
			response.SecurityParameters = &gosnmp.UsmSecurityParameters{AuthoritativeEngineID: snmpFixtureEngine, AuthoritativeEngineBoots: 3, AuthoritativeEngineTime: 100, AuthenticationProtocol: gosnmp.NoAuth, PrivacyProtocol: gosnmp.NoPriv}
			response.Variables = []gosnmp.SnmpPDU{{Name: ".1.3.6.1.6.3.15.1.1.4.0", Type: gosnmp.Counter32, Value: uint32(1)}}
		} else {
			if request.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv {
				t.Error("request downgraded AuthPriv")
				continue
			}
			if bytes.Contains(wire, []byte(snmpFixtureAuth)) || bytes.Contains(wire, []byte(snmpFixturePrivacy)) {
				t.Error("password appeared on wire")
				continue
			}
			if err := verifySNMPWireHMAC(wire, p.security); err != nil {
				t.Errorf("independent HMAC: %v", err)
				continue
			}
			// A known scoped OID must not occur in the encrypted wire payload.
			oid, _ := asn1.Marshal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 0})
			if bytes.Contains(wire, oid) {
				t.Error("scoped PDU was sent as plaintext")
				continue
			}
			if len(request.Variables) != 1 {
				t.Error("unexpected variable count")
				continue
			}
			response.Variables = []gosnmp.SnmpPDU{{Name: request.Variables[0].Name, Type: gosnmp.Integer, Value: 1}}
			switch request.PDUType {
			case gosnmp.GetRequest:
				p.mu.Lock()
				p.reads++
				p.mu.Unlock()
				if p.mode == "preflight_cleartext" {
					response.MsgFlags = gosnmp.AuthNoPriv
				}
				if p.mode == "preflight_wrong_type" {
					response.Variables[0].Type = gosnmp.OctetString
					response.Variables[0].Value = "not-state"
				}
			case gosnmp.SetRequest:
				p.mu.Lock()
				p.writes++
				p.mu.Unlock()
				response.Variables[0].Value = request.Variables[0].Value
				if p.mode == "drop" {
					continue
				}
				switch p.mode {
				case "wrong_oid":
					response.Variables[0].Name = snmpFixtureStateOID
				case "wrong_type":
					response.Variables[0].Type = gosnmp.OctetString
					response.Variables[0].Value = "invalid"
				case "wrong_value":
					response.Variables[0].Value = 99
				case "agent_error":
					response.Error = gosnmp.NotWritable
					response.ErrorIndex = 1
				case "cleartext":
					response.MsgFlags = gosnmp.AuthNoPriv
				case "wrong_user":
					response.SecurityParameters.(*gosnmp.UsmSecurityParameters).UserName = "other-user"
				case "time_report", "engine_report":
					response.PDUType = gosnmp.Report
					oid := ".1.3.6.1.6.3.15.1.1.2.0"
					if p.mode == "engine_report" {
						oid = ".1.3.6.1.6.3.15.1.1.4.0"
					}
					response.Variables = []gosnmp.SnmpPDU{{Name: oid, Type: gosnmp.Counter32, Value: uint32(1)}}
				}
			default:
				t.Errorf("unexpected PDU %v", request.PDUType)
				continue
			}
		}
		if err := p.security.InitPacket(response); err != nil {
			t.Errorf("fixture salt: %v", err)
			continue
		}
		encoded, err := response.MarshalMsg()
		if err != nil {
			t.Errorf("fixture marshal: %v", err)
			continue
		}
		if p.mode == "bad_auth" && request.PDUType == gosnmp.SetRequest {
			encoded[len(encoded)-1] ^= 1
		}
		if _, err = p.conn.WriteToUDP(encoded, addr); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("fixture send: %v", err)
		}
	}
}

// RFC 3414/7860 authentication is independently checked over the full datagram.
func verifySNMPWireHMAC(wire []byte, security *gosnmp.UsmSecurityParameters) error {
	var envelope struct {
		Version  int
		Header   asn1.RawValue
		Security []byte
		Scoped   asn1.RawValue
	}
	if rest, err := asn1.Unmarshal(wire, &envelope); err != nil || len(rest) != 0 {
		return errors.New("bad envelope")
	}
	var usm struct {
		Engine  []byte
		Boots   int
		Time    int
		User    []byte
		Auth    []byte
		Privacy []byte
	}
	if rest, err := asn1.Unmarshal(envelope.Security, &usm); err != nil || len(rest) != 0 {
		return errors.New("bad USM")
	}
	authTLV, _ := asn1.Marshal(usm.Auth)
	pos := bytes.Index(wire, authTLV)
	if pos < 0 || len(usm.Auth) == 0 {
		return errors.New("missing HMAC")
	}
	zeroed := append([]byte(nil), wire...)
	clear(zeroed[pos+len(authTLV)-len(usm.Auth) : pos+len(authTLV)])
	mac := hmac.New(security.AuthenticationProtocol.HashType().New, security.SecretKey)
	_, _ = mac.Write(zeroed)
	if !hmac.Equal(usm.Auth, mac.Sum(nil)[:len(usm.Auth)]) {
		return errors.New("HMAC mismatch")
	}
	return nil
}

func TestSNMPv3UDPAlgorithmsReadWrite(t *testing.T) {
	for _, authName := range []string{"SHA", "SHA224", "SHA256", "SHA384", "SHA512"} {
		for _, privacyName := range []string{"AES", "AES192", "AES256", "AES192C", "AES256C"} {
			t.Run(authName+"/"+privacyName, func(t *testing.T) {
				auth, privacy, err := ParseSNMPv3Algorithms(authName, privacyName)
				if err != nil {
					t.Fatal(err)
				}
				peer, transport, target := newSNMPWirePeer(t, auth, privacy, "")
				value, err := transport.GetInteger(context.Background(), target, snmpFixtureStateOID)
				if err != nil || value != 1 {
					t.Fatalf("encrypted GET: value=%d error=%v", value, err)
				}
				if err := transport.SetInteger(context.Background(), target, snmpFixtureControlOID, 2); err != nil {
					t.Fatal(err)
				}
				discoveries, reads, writes := peer.counts()
				if discoveries != 2 || reads != 2 || writes != 1 {
					t.Fatalf("unexpected exchanges: discovery=%d read=%d write=%d", discoveries, reads, writes)
				}
			})
		}
	}
}

func TestSNMPv3UDPWriteFailuresNeverResend(t *testing.T) {
	for _, mode := range []string{"drop", "bad_auth", "wrong_user", "time_report", "engine_report", "wrong_oid", "wrong_type", "wrong_value", "agent_error", "cleartext", "preflight_cleartext", "preflight_wrong_type"} {
		t.Run(mode, func(t *testing.T) {
			peer, transport, target := newSNMPWirePeer(t, gosnmp.SHA256, gosnmp.AES, mode)
			err := transport.SetInteger(context.Background(), target, snmpFixtureControlOID, 2)
			if err == nil {
				t.Fatal("invalid or ambiguous result accepted")
			}
			if strings.Contains(err.Error(), snmpFixtureAuth) || strings.Contains(err.Error(), snmpFixturePrivacy) || strings.Contains(err.Error(), target.Address) {
				t.Fatal("sensitive details leaked")
			}
			discoveries, reads, writes := peer.counts()
			wantWrites := 1
			if strings.HasPrefix(mode, "preflight_") {
				wantWrites = 0
			}
			if discoveries != 1 || reads != 1 || writes != wantWrites {
				t.Fatalf("write repeated or preflight bypassed: discovery=%d read=%d write=%d", discoveries, reads, writes)
			}
		})
	}
}

// Independent RFC 3414 password localization, deliberately without gosnmp's
// key helpers; extension variants differ only when the hash is too short.
func snmpLocalizedKey(hash crypto.Hash, password []byte, engine []byte) []byte {
	buf := make([]byte, 1<<20)
	for i := range buf {
		buf[i] = password[i%len(password)]
	}
	h := hash.New()
	_, _ = h.Write(buf)
	ku := h.Sum(nil)
	h.Reset()
	_, _ = h.Write(ku)
	_, _ = h.Write(engine)
	_, _ = h.Write(ku)
	return h.Sum(nil)
}

func TestSNMPv3AESKeyExtensionDistinct(t *testing.T) {
	for _, auth := range []gosnmp.SnmpV3AuthProtocol{gosnmp.SHA, gosnmp.SHA224, gosnmp.SHA256, gosnmp.SHA384, gosnmp.SHA512} {
		for _, privacy := range []gosnmp.SnmpV3PrivProtocol{gosnmp.AES, gosnmp.AES192, gosnmp.AES256, gosnmp.AES192C, gosnmp.AES256C} {
			sp := &gosnmp.UsmSecurityParameters{AuthenticationProtocol: auth, PrivacyProtocol: privacy, AuthenticationPassphrase: snmpFixtureAuth, PrivacyPassphrase: snmpFixturePrivacy, AuthoritativeEngineID: snmpFixtureEngine}
			if err := sp.InitSecurityKeys(); err != nil {
				t.Fatal(err)
			}
			localized := snmpLocalizedKey(auth.HashType(), []byte(snmpFixturePrivacy), []byte(snmpFixtureEngine))
			var extension []byte
			if privacy == gosnmp.AES192 || privacy == gosnmp.AES256 {
				h := auth.HashType().New()
				_, _ = h.Write(localized)
				extension = h.Sum(nil)
			} else {
				extension = snmpLocalizedKey(auth.HashType(), localized, []byte(snmpFixtureEngine))
			}
			length := 16
			if privacy == gosnmp.AES192 || privacy == gosnmp.AES192C {
				length = 24
			}
			if privacy == gosnmp.AES256 || privacy == gosnmp.AES256C {
				length = 32
			}
			if !bytes.Equal(sp.PrivacyKey, append(localized, extension...)[:length]) {
				t.Fatalf("key extension mismatch %s/%s", auth, privacy)
			}
		}
	}
	base := snmpLocalizedKey(crypto.SHA1, []byte(snmpFixturePrivacy), []byte(snmpFixtureEngine))
	h := crypto.SHA1.New()
	_, _ = h.Write(base)
	if bytes.Equal(h.Sum(nil)[:12], snmpLocalizedKey(crypto.SHA1, base, []byte(snmpFixtureEngine))[:12]) {
		t.Fatal("fixture did not distinguish extension variants")
	}
}
