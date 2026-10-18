package drivers

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func TestSNMPv3RejectInvalidAlgorithmsBeforeNetwork(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target := Target{Address: listener.LocalAddr().String()}
	for i := 0; i < 256; i++ {
		for _, authMode := range []bool{true, false} {
			auth, privacy := gosnmp.SHA256, gosnmp.AES
			if authMode {
				auth = gosnmp.SnmpV3AuthProtocol(i)
			} else {
				privacy = gosnmp.SnmpV3PrivProtocol(i)
			}
			validationErr := ValidateSNMPv3Security("offline-user", snmpFixtureAuth, snmpFixturePrivacy, auth, privacy)
			// Locked gosnmp v1.45.0 assigns 3..7 to the five approved enums
			// in each family. All other uint8 values must be rejected.
			if i >= 3 && i <= 7 {
				if validationErr != nil {
					t.Fatalf("approved enum rejected %d: %v", i, validationErr)
				}
				continue
			}
			if validationErr == nil {
				t.Fatalf("unknown/disabled enum accepted %d auth=%v", i, authMode)
			}
			transport := SNMPV3Transport{AllowLoopback: true, Username: "offline-user", AuthPassword: snmpFixtureAuth, PrivacyPassword: snmpFixturePrivacy, AuthProtocol: auth, PrivacyProtocol: privacy}
			if _, err := transport.GetInteger(context.Background(), target, snmpFixtureStateOID); err == nil {
				t.Fatalf("invalid enum accepted %d auth=%v", i, authMode)
			}
		}
	}
	for _, names := range [][2]string{{"", "AES"}, {"SHA256", ""}, {"MD5", "AES"}, {"SHA256", "DES"}, {"sha256", "AES"}, {"SHA256", "aes"}, {"SHA256", "AES128"}, {" SHA", "AES"}, {"SHA", "AES256Cisco"}} {
		if _, _, err := ParseSNMPv3Algorithms(names[0], names[1]); err == nil {
			t.Fatalf("implicit or unsupported algorithm accepted: %v", names)
		}
	}
	_ = listener.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := listener.ReadFromUDP(make([]byte, 65535)); err == nil {
		t.Fatal("invalid configuration emitted a datagram")
	}
}

func TestSNMPv3CredentialBoundsAndResponseSecurity(t *testing.T) {
	for _, creds := range [][3]string{{"", snmpFixtureAuth, snmpFixturePrivacy}, {strings.Repeat("u", 33), snmpFixtureAuth, snmpFixturePrivacy}, {"user", "short", snmpFixturePrivacy}, {"user", snmpFixtureAuth, "short"}, {"user", strings.Repeat("s", 1025), snmpFixturePrivacy}, {"user", snmpFixtureAuth, "secret\x00value"}} {
		if err := ValidateSNMPv3Security(creds[0], creds[1], creds[2], gosnmp.SHA256, gosnmp.AES); err == nil {
			t.Fatal("invalid credential bounds accepted")
		}
	}
	valid := gosnmp.SnmpPacket{Version: gosnmp.Version3, SecurityModel: gosnmp.UserSecurityModel, MsgFlags: gosnmp.AuthPriv, PDUType: gosnmp.GetResponse}
	if err := ValidateSNMPv3Response(&valid); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "model", "auth", "privacy", "pdu", "nil"} {
		p := valid
		switch field {
		case "version":
			p.Version = gosnmp.Version2c
		case "model":
			p.SecurityModel = 0
		case "auth":
			p.MsgFlags = gosnmp.NoAuthNoPriv
		case "privacy":
			p.MsgFlags = gosnmp.AuthNoPriv
		case "pdu":
			p.PDUType = gosnmp.Report
		}
		pointer := &p
		if field == "nil" {
			pointer = nil
		}
		if err := ValidateSNMPv3Response(pointer); err == nil {
			t.Fatalf("insecure response accepted: %s", field)
		}
	}
}
