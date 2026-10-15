package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func TestMockStateAndFaults(t *testing.T) {
	m := NewMock()
	target := Target{DeviceID: "dev", Kind: "mock_pikvm", Port: 1}
	if r := m.Execute(context.Background(), target, "power_on"); !r.Completed || r.PowerState != "on" {
		t.Fatalf("power on: %+v", r)
	}
	if r := m.Execute(context.Background(), target, "status"); r.PowerState != "on" {
		t.Fatalf("state not retained: %+v", r)
	}
	if r := m.Execute(context.Background(), target, "hard_reset"); !r.Accepted || r.Completed || r.TargetReached || r.Status != "unknown" {
		t.Fatalf("reset falsely completed: %+v", r)
	}
	if r := m.Execute(context.Background(), target, "disable_port"); r.CooldownSeconds < 30 || r.PowerState != "off" {
		t.Fatalf("maintenance: %+v", r)
	}
	if r := m.Execute(context.Background(), target, "power_on"); r.Code != "PORT_DISABLED" {
		t.Fatalf("disabled power on: %+v", r)
	}
	for _, s := range []string{"offline", "inner_error", "timeout", "busy"} {
		target.Scenario = s
		r := m.Execute(context.Background(), target, "power_on")
		if r.Status == "succeeded" {
			t.Errorf("fault %s falsely succeeded", s)
		}
	}
	target.Scenario = "timeout"
	if r := m.Execute(context.Background(), target, "enable_port"); r.CooldownSeconds < 30 {
		t.Fatal("ambiguous maintenance missed cooldown")
	}
	pdu := Target{DeviceID: "pdu", Kind: "mock_pdu", Port: 1}
	if r := m.Execute(context.Background(), pdu, "power_on"); r.SupplyState != "on" || r.PowerState != "unknown" {
		t.Fatalf("PDU inferred server state: %+v", r)
	}
	if r := m.Execute(context.Background(), pdu, "request_shutdown"); r.Code != "CAPABILITY_UNSUPPORTED" {
		t.Fatal("PDU graceful shutdown misrepresented")
	}
}

func TestPiKVMRejectsInnerFailures(t *testing.T) {
	for _, body := range []string{`{"ok":true,"result":{"status":1,"res":"failure"}}`, `{"ok":true,"result":{"error":"secret password"}}`, `{"ok":true,"result":{}}`, `{"ok":true,"result":{"status":null,"res":""}}`, `{"ok":true,"result":{"status":"0","res":""}}`, `{"ok":true,"result":{"status":0}}`, `<html>login</html>`, `{"result":{"status":0,"res":""}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}})
			r := d.Execute(context.Background(), Target{Address: server.URL, Port: 1}, "power_on")
			if r.Status == "succeeded" || r.Accepted || r.Completed || r.TargetReached {
				t.Fatalf("false success: %+v", r)
			}
			if r.Message == "secret password" {
				t.Fatal("leaked device response")
			}
		})
	}
}

func chassisFixture(state string) []byte {
	rows := map[string]string{}
	for i := 1; i <= 16; i++ {
		rows[strconv.Itoa(i)] = "ONL |ENB |" + state
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"num": 16, "res": rows}})
	return b
}

func TestPiKVMRequestAndCompletion(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-KVMD-User") != "u" || r.Header.Get("X-KVMD-Passwd") != "p" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("incorrect authentication forwarding")
		}
		if r.Method == "POST" {
			posts.Add(1)
			if r.URL.Query().Get("num") != "16" || len(r.URL.Query()) != 1 {
				t.Error("port query is not one integer")
			}
			fmt.Fprint(w, `{"ok":true,"result":{"status":0,"res":"ok"}}`)
			return
		}
		w.Write(chassisFixture("ON"))
	}))
	defer server.Close()
	d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}, MappingVerified: true})
	target := Target{Address: server.URL, Port: 16, Username: "u", Password: "p"}
	if r := d.Execute(context.Background(), target, "power_on"); !r.Accepted || !r.Completed || !r.TargetReached {
		t.Fatalf("not verified: %+v", r)
	}
	if r := d.Execute(context.Background(), target, "hard_reset"); r.Completed || r.TargetReached || !r.Accepted {
		t.Fatalf("reset inferred from ON: %+v", r)
	}
	if r := d.Execute(context.Background(), target, "enable_port"); r.CooldownSeconds != 30 || r.Completed {
		t.Fatalf("maintenance: %+v", r)
	}
	target.Port = 17
	if r := d.Execute(context.Background(), target, "power_on"); r.Code != "INVALID_PORT" {
		t.Fatal("out-of-range accepted")
	}
	if posts.Load() != 3 {
		t.Fatalf("unexpected POST count %d", posts.Load())
	}
}

func TestPiKVMTimeoutSentOnce(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer server.Close()
	d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}, Timeout: 30 * time.Millisecond})
	r := d.Execute(context.Background(), Target{Address: server.URL, Port: 1}, "disable_port")
	if r.Status != "unknown" || r.Completed || r.CooldownSeconds < 30 || calls.Load() != 1 {
		t.Fatalf("timeout retry or certainty: %+v, calls %d", r, calls.Load())
	}
}

func TestPiKVMSelectedPortAndCompleteness(t *testing.T) {
	for _, data := range []string{`{"ok":true,"result":{"error":"failed","kvm_status":1}}`, `{"ok":true,"result":{"kvm_status":17}}`, `{"ok":true,"result":{"kvm_status":"1; id"}}`, `{"ok":true,"result":{"kvm_status":1.5}}`, `{"ok":true,"result":{}}`} {
		if p, err := ParseSelectedPort([]byte(data)); err == nil || p != 0 {
			t.Fatalf("untrusted selection: %d %v", p, err)
		}
	}
	if p, err := ParseSelectedPort([]byte(`{"ok":true,"result":{"kvm_status":" 16 "}}`)); err != nil || p != 16 {
		t.Fatalf("valid selection: %d %v", p, err)
	}
	if _, err := ParseChassisType(chassisFixture("N/A")); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseChassisType(chassisFixture("bad")); err == nil {
		t.Fatal("unknown state accepted")
	}
	if _, err := ParseChassisType([]byte(`{"ok":true,"result":{"num":16,"res":{"1":"ONL|ENB|ON"}}}`)); err == nil {
		t.Fatal("missing ports accepted")
	}
}

func TestNetworkDefaultDenyAndRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "http://192.0.2.1/", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	target := Target{Address: server.URL, Port: 1}
	if r := NewPiKVM(PiKVMConfig{}).Execute(context.Background(), target, "status"); r.Code != "DEVICE_ACCESS_DISABLED" || calls.Load() != 0 {
		t.Fatal("default network contacted server")
	}
	d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}})
	if r := d.Execute(context.Background(), target, "power_on"); r.Accepted || calls.Load() != 1 {
		t.Fatal("redirect followed or accepted")
	}
	for _, address := range []string{"http://192.0.2.1", "http://localhost", "http://127.0.0.1@192.0.2.1", "http://127.0.0.1/api", "http://127.0.0.1?num=1"} {
		if _, err := (NetworkPolicy{AllowLoopback: true, AllowWrite: true}).validate(address, true); err == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
}

func redfishFixture(values []string, resetTarget string) []byte {
	b, _ := json.Marshal(map[string]any{"@odata.id": "/redfish/v1/Systems/test", "PowerState": "On", "Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": resetTarget, "ResetType@Redfish.AllowableValues": values}}})
	return b
}
func TestRedfishCapabilitiesAndResetEvidence(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write(redfishFixture([]string{"On", "ForceRestart"}, "/redfish/v1/Systems/test/Actions/ComputerSystem.Reset"))
			return
		}
		posts.Add(1)
		var payload map[string]string
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["ResetType"] != "ForceRestart" {
			t.Error("wrong reset payload")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	d := NewRedfish(RedfishConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}, SystemPath: "/redfish/v1/Systems/test"})
	target := Target{Address: server.URL}
	if r := d.Execute(context.Background(), target, "request_shutdown"); r.Code != "CAPABILITY_UNSUPPORTED" {
		t.Fatalf("unsupported capability: %+v", r)
	}
	if r := d.Execute(context.Background(), target, "hard_reset"); !r.Accepted || r.Completed || r.TargetReached || r.Status != "unknown" {
		t.Fatalf("false reboot completion: %+v", r)
	}
	if posts.Load() != 1 {
		t.Fatalf("unexpected writes %d", posts.Load())
	}
}

func TestRedfishRejectsCrossSystemTarget(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
		}
		w.Write(redfishFixture([]string{"On"}, "/redfish/v1/Systems/other/Actions/ComputerSystem.Reset"))
	}))
	defer server.Close()
	d := NewRedfish(RedfishConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}, SystemPath: "/redfish/v1/Systems/test"})
	if r := d.Execute(context.Background(), Target{Address: server.URL}, "power_on"); r.Code != "CAPABILITY_UNSUPPORTED" || posts.Load() != 0 {
		t.Fatalf("cross system write: %+v", r)
	}
}

func TestRedfishActionInfoDiscoveryAndTargetVerification(t *testing.T) {
	var powered atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redfish/v1/Systems":
			fmt.Fprint(w, `{"Members":[{"@odata.id":"/redfish/v1/Systems/test"}]}`)
		case "/redfish/v1/Systems/test":
			state := "Off"
			if powered.Load() {
				state = "On"
			}
			json.NewEncoder(w).Encode(map[string]any{"@odata.id": "/redfish/v1/Systems/test", "PowerState": state, "Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{"target": "/redfish/v1/Systems/test/Actions/ComputerSystem.Reset", "@Redfish.ActionInfo": "/redfish/v1/Systems/test/ResetActionInfo"}}})
		case "/redfish/v1/Systems/test/ResetActionInfo":
			fmt.Fprint(w, `{"Parameters":[{"Name":"ResetType","AllowableValues":["On"]}]}`)
		case "/redfish/v1/Systems/test/Actions/ComputerSystem.Reset":
			if r.Method != "POST" {
				t.Error("reset must be POST")
			}
			powered.Store(true)
			w.WriteHeader(204)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	d := NewRedfish(RedfishConfig{Network: NetworkPolicy{AllowLoopback: true, AllowWrite: true}, SystemPath: "/redfish/v1/Systems/test"})
	target := Target{Address: server.URL}
	paths, err := d.DiscoverSystems(context.Background(), target)
	if err != nil || len(paths) != 1 || powered.Load() {
		t.Fatal("discovery changed power state")
	}
	if r := d.Execute(context.Background(), target, "power_on"); !r.Accepted || !r.Completed || !r.TargetReached || r.PowerState != "on" {
		t.Fatalf("power state not verified: %+v", r)
	}
}

type fakeSNMP struct {
	gets, sets int
	state      int
	fail       bool
}

func (f *fakeSNMP) GetInteger(context.Context, Target, string) (int, error) {
	f.gets++
	return f.state, nil
}
func (f *fakeSNMP) SetInteger(_ context.Context, _ Target, _ string, value int) error {
	f.sets++
	if f.fail {
		return errors.New("timeout")
	}
	f.state = value
	return nil
}
func TestAPCProfileAndSupplySeparation(t *testing.T) {
	f := &fakeSNMP{state: 2}
	d := &APC{Transport: f, AllowReads: true, AllowWrites: true}
	target := Target{Port: 1}
	if r := d.Execute(context.Background(), target, "power_on"); r.Code != "CAPABILITY_UNVERIFIED" || f.sets != 0 {
		t.Fatal("unverified profile invoked transport")
	}
	// Synthetic test-only OIDs; not an APC OID mapping.
	d.Profile = APCProfile{Confirmed: true, Model: "fixture", Firmware: "fixture", Evidence: "offline fixture", Outlets: map[int]APCOutlet{1: {StateOID: "1.3.6.1.4.1.99999.1.1", ControlOID: "1.3.6.1.4.1.99999.2.1", StateValues: map[int]string{1: "on", 2: "off"}, Actions: map[string]int{"power_on": 1, "hard_power_off": 2, "power_cycle": 3}}}}
	if r := d.Execute(context.Background(), target, "power_on"); !r.Completed || r.PowerState != "unknown" || r.SupplyState != "on" {
		t.Fatalf("supply/server confusion: %+v", r)
	}
	if r := d.Execute(context.Background(), target, "power_cycle"); !r.Accepted || r.Completed || r.TargetReached {
		t.Fatalf("cycle inferred from supply ON: %+v", r)
	}
	f.fail = true
	if r := d.Execute(context.Background(), target, "hard_power_off"); r.Status != "unknown" || f.sets != 3 {
		t.Fatalf("SET timeout retried: %+v writes=%d", r, f.sets)
	}
	if r := d.Execute(context.Background(), target, "request_shutdown"); r.Code != "CAPABILITY_UNSUPPORTED" {
		t.Fatal("cut substituted for graceful shutdown")
	}
}

type countingConn struct {
	net.Conn
	writes int
}

func (c *countingConn) Write(b []byte) (int, error) { c.writes++; return len(b), nil }
func TestSNMPWriteFenceAndValidation(t *testing.T) {
	c := &countingConn{}
	fence := &oneWriteConn{Conn: c}
	if _, err := fence.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := fence.Write([]byte{1}); err == nil || c.writes != 1 {
		t.Fatal("SNMP SET could be resent")
	}
	s := &SNMPV3Transport{}
	if _, err := s.session(context.Background(), Target{Address: "127.0.0.1:161"}, false); err == nil {
		t.Fatal("default SNMP access enabled")
	}
	p := &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{{Name: "1.3.6.1", Type: gosnmp.OctetString, Value: "1"}}}
	if _, err := integerPacket(p, "1.3.6.1"); err == nil {
		t.Fatal("SNMP wrong type accepted")
	}
	if r := (IPMI{}).Execute(context.Background(), Target{}, "power_on"); r.Code != "CAPABILITY_UNSUPPORTED" {
		t.Fatal("unverified IPMI supported")
	}
}
