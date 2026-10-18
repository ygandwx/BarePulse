package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func snmpTrafficFixtureCredentials() SNMPTrafficCredentials {
	return SNMPTrafficCredentials{Username: "offline-user", AuthProtocol: "SHA224", PrivacyProtocol: "AES256C", AuthPassword: "offline-auth-passphrase", PrivacyPassword: "offline-privacy-passphrase", ContextName: "offline-context"}
}

func TestSNMPConfigExplicitAlgorithmsAndPreNetworkErrors(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, field := range []string{"auth_missing", "privacy_missing", "MD5", "DES", "unknown", "short_password", "disabled"} {
		t.Run(field, func(t *testing.T) {
			c := SNMPTrafficCollector{Allowed: true, Address: listener.LocalAddr().String(), Credentials: snmpTrafficFixtureCredentials()}
			switch field {
			case "auth_missing":
				c.Credentials.AuthProtocol = ""
			case "privacy_missing":
				c.Credentials.PrivacyProtocol = ""
			case "MD5":
				c.Credentials.AuthProtocol = "MD5"
			case "DES":
				c.Credentials.PrivacyProtocol = "DES"
			case "unknown":
				c.Credentials.PrivacyProtocol = "private-secret-unsupported"
			case "short_password":
				c.Credentials.AuthPassword = "short"
			case "disabled":
				c.Allowed = false
			}
			_, err := c.Collect(context.Background(), TrafficSource{ExpectedIfIndex: 7})
			var api *apiError
			if !errors.As(err, &api) || api.Status != 409 {
				t.Fatalf("invalid configuration not rejected: %v", err)
			}
			if strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), c.Credentials.AuthPassword) || strings.Contains(err.Error(), listener.LocalAddr().String()) {
				t.Fatal("configuration error leaks private values")
			}
		})
	}
	for _, auth := range []string{"SHA", "SHA224", "SHA256", "SHA384", "SHA512"} {
		for _, privacy := range []string{"AES", "AES192", "AES256", "AES192C", "AES256C"} {
			creds := snmpTrafficFixtureCredentials()
			creds.AuthProtocol, creds.PrivacyProtocol = auth, privacy
			collector := SNMPTrafficCollector{Allowed: true, Address: listener.LocalAddr().String(), Credentials: creds}
			client, err := collector.client(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			security := client.SecurityParameters.(*gosnmp.UsmSecurityParameters)
			if security.AuthenticationProtocol.String() != auth || security.PrivacyProtocol.String() != privacy || client.Retries != 0 || client.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv {
				t.Fatal("traffic algorithm silently replaced")
			}
			_ = client.Close()
			powerAuth, powerPrivacy, err := snmpPowerAlgorithms(&PowerSNMPProfile{AuthProtocol: auth, PrivacyProtocol: privacy, AuthPassword: creds.AuthPassword, PrivacyPassword: creds.PrivacyPassword, DiscoveryOID: ".1.3.6.1.4.1.99999.1.0"})
			if err != nil || powerAuth != security.AuthenticationProtocol || powerPrivacy != security.PrivacyProtocol {
				t.Fatalf("power/traffic algorithm drift: %v", err)
			}
		}
	}
	_ = listener.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := listener.ReadFromUDP(make([]byte, 65535)); err == nil {
		t.Fatal("configuration-only checks contacted peer")
	}
}

// Actual local UDP engine discovery and encrypted counter/discovery responses,
// deliberately configured as SHA224/AES256C to catch hardcoded SHA256/AES.
func TestSNMPTrafficUDPCollectAndDiscover(t *testing.T) {
	for _, mode := range []string{"normal", "paginated", "cleartext", "wrong_type", "nonincreasing", "missing_object"} {
		t.Run(mode, func(t *testing.T) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
			if err != nil {
				t.Fatal(err)
			}
			creds := snmpTrafficFixtureCredentials()
			const engine = "\x80\x00\x00\x00\x05traffic-fixture"
			security := &gosnmp.UsmSecurityParameters{UserName: creds.Username, AuthenticationProtocol: gosnmp.SHA224, PrivacyProtocol: gosnmp.AES256C, AuthenticationPassphrase: creds.AuthPassword, PrivacyPassphrase: creds.PrivacyPassword, AuthoritativeEngineID: engine, AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 10}
			if err := security.InitSecurityKeys(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				decoder := &gosnmp.GoSNMP{Version: gosnmp.Version3, MsgFlags: gosnmp.AuthPriv, SecurityModel: gosnmp.UserSecurityModel, SecurityParameters: security}
				buffer := make([]byte, 65535)
				for {
					n, addr, err := conn.ReadFromUDP(buffer)
					if err != nil {
						return
					}
					req, err := decoder.UnmarshalTrap(append([]byte(nil), buffer[:n]...), true)
					if err != nil {
						t.Errorf("UDP fixture authentication: %v", err)
						continue
					}
					response := &gosnmp.SnmpPacket{Version: gosnmp.Version3, MsgFlags: gosnmp.AuthPriv, SecurityModel: gosnmp.UserSecurityModel, PDUType: gosnmp.GetResponse, MsgID: req.MsgID, RequestID: req.RequestID, MsgMaxSize: 65507, ContextEngineID: engine, ContextName: req.ContextName, SecurityParameters: security.Copy()}
					if req.SecurityParameters.(*gosnmp.UsmSecurityParameters).UserName == "" {
						response.PDUType = gosnmp.Report
						response.MsgFlags = gosnmp.NoAuthNoPriv
						response.SecurityParameters = &gosnmp.UsmSecurityParameters{AuthoritativeEngineID: engine, AuthoritativeEngineBoots: 1, AuthoritativeEngineTime: 10, AuthenticationProtocol: gosnmp.NoAuth, PrivacyProtocol: gosnmp.NoPriv}
						response.Variables = []gosnmp.SnmpPDU{{Name: ".1.3.6.1.6.3.15.1.1.4.0", Type: gosnmp.Counter32, Value: uint32(1)}}
					} else {
						if req.ContextName != creds.ContextName || req.MsgFlags&gosnmp.AuthPriv != gosnmp.AuthPriv {
							t.Error("wrong context/security")
						}
						switch req.PDUType {
						case gosnmp.GetRequest:
							oids := trafficOIDs(7)
							if len(req.Variables) != len(oids) {
								t.Error("wrong counter request size")
								continue
							}
							for i, pdu := range req.Variables {
								if "."+strings.TrimPrefix(pdu.Name, ".") != oids[i] {
									t.Error("wrong counter OID")
								}
							}
							response.Variables = []gosnmp.SnmpPDU{{Name: oids[0], Type: gosnmp.TimeTicks, Value: uint32(12345)}, {Name: oids[1], Type: gosnmp.OctetString, Value: "eth7"}, {Name: oids[2], Type: gosnmp.Counter64, Value: uint64(9007199254740993)}, {Name: oids[3], Type: gosnmp.Counter64, Value: uint64(18014398509481987)}, {Name: oids[4], Type: gosnmp.TimeTicks, Value: uint32(0)}, {Name: oids[5], Type: gosnmp.OctetString, Value: "offline"}, {Name: oids[6], Type: gosnmp.Integer, Value: 1}}
						case gosnmp.GetBulkRequest:
							response.Variables = []gosnmp.SnmpPDU{{Name: trafficIfNameOID + ".7", Type: gosnmp.OctetString, Value: "eth7"}, {Name: trafficIfNameOID + ".9", Type: gosnmp.OctetString, Value: "eth9"}, {Name: trafficIfNameOID + ".9", Type: gosnmp.EndOfMibView, Value: nil}}
							if mode == "paginated" {
								if "."+strings.TrimPrefix(req.Variables[0].Name, ".") == trafficIfNameOID {
									response.Variables = response.Variables[:1]
								} else if "."+strings.TrimPrefix(req.Variables[0].Name, ".") == trafficIfNameOID+".7" {
									response.Variables = response.Variables[1:]
								} else {
									t.Error("discovery cursor did not advance to last interface")
								}
							}
							if mode == "nonincreasing" {
								response.Variables[1].Name = trafficIfNameOID + ".7"
							}
							if mode == "missing_object" {
								response.Variables = []gosnmp.SnmpPDU{{Name: trafficIfNameOID, Type: gosnmp.NoSuchObject, Value: nil}}
							}
						default:
							t.Errorf("unexpected write/PDU: %v", req.PDUType)
							continue
						}
						if mode == "cleartext" {
							response.MsgFlags = gosnmp.AuthNoPriv
						}
						if mode == "wrong_type" {
							response.Variables[0].Type = gosnmp.Integer
							response.Variables[0].Value = 0
						}
					}
					if err := security.InitPacket(response); err != nil {
						t.Error(err)
						continue
					}
					out, err := response.MarshalMsg()
					if err != nil {
						t.Error(err)
						continue
					}
					if _, err = conn.WriteToUDP(out, addr); err != nil && !errors.Is(err, net.ErrClosed) {
						t.Error(err)
					}
				}
			}()
			t.Cleanup(func() { _ = conn.Close(); <-done })
			collector := SNMPTrafficCollector{Allowed: true, Address: conn.LocalAddr().String(), Credentials: creds}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sample, err := collector.Collect(ctx, TrafficSource{ExpectedIfIndex: 7, BindingVersion: 4})
			if mode != "cleartext" && mode != "wrong_type" {
				if err != nil || sample.InOctets != "9007199254740993" || sample.OutOctets != "18014398509481987" || sample.IfName != "eth7" || sample.LinkState != "up" || sample.BindingVersion != 4 {
					t.Fatalf("counter collection mismatch: %+v %v", sample, err)
				}
			} else if err == nil {
				t.Fatal("invalid counter response accepted")
			}
			interfaces, err := collector.Discover(ctx)
			if mode == "normal" || mode == "paginated" {
				if err != nil || len(interfaces) != 2 || interfaces[1].IfIndex != 9 {
					t.Fatalf("interface discovery: %+v %v", interfaces, err)
				}
			} else if err == nil {
				t.Fatal("invalid discovery accepted")
			}
		})
	}
}
