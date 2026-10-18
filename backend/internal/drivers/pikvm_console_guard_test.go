package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPiKVMConsoleGuardLatchesDriftAndUnknown(t *testing.T) {
	for _, response := range []string{
		`{"ok":true,"result":{"kvm_status":"2"}}`,
		`{"ok":true,"result":{"kvm_status":1,"error":"private diagnostic"}}`,
		`{"ok":true,"result":{}}`,
	} {
		t.Run(response, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RequestURI() != "/api/chassis_status" {
					t.Errorf("unexpected device request %s %s", r.Method, r.URL.RequestURI())
				}
				n := calls.Add(1)
				if n == 2 {
					fmt.Fprint(w, response)
				} else {
					fmt.Fprint(w, `{"ok":true,"result":{"kvm_status":"1"}}`)
				}
			}))
			defer server.Close()
			d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: true}, MappingVerified: true})
			g, err := d.WatchConsoleTarget(context.Background(), Target{Address: server.URL, Port: 1}, 250*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			select {
			case <-g.Context().Done():
			case <-time.After(3 * time.Second):
				t.Fatal("target change did not revoke the guard")
			}
			cause := context.Cause(g.Context())
			if response == `{"ok":true,"result":{"kvm_status":"2"}}` {
				if !errors.Is(cause, ErrConsoleTargetChanged) {
					t.Fatal(cause)
				}
			} else if !errors.Is(cause, ErrConsoleTargetUnknown) {
				t.Fatal(cause)
			}
			g.Close()
			if calls.Load() != 2 {
				t.Fatalf("unexpected recovery or writes: %d", calls.Load())
			}
		})
	}
}

func TestPiKVMConsoleGuardCloseCancelsInFlightRead(t *testing.T) {
	var calls atomic.Int32
	reading := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true,"result":{"kvm_status":1}}`)
			return
		}
		close(reading)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: true}, MappingVerified: true, Timeout: 5 * time.Second})
	g, err := d.WatchConsoleTarget(context.Background(), Target{Address: server.URL, Port: 1}, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	select {
	case <-reading:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not start")
	}
	g.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("HTTP read remained active after Close")
	}
	if !errors.Is(context.Cause(g.Context()), ErrConsoleGuardClosed) {
		t.Fatal(context.Cause(g.Context()))
	}
}

func TestPiKVMConsoleGuardRejectsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	for _, tc := range []struct {
		mapping, access bool
		port            int
		interval        time.Duration
	}{
		{false, true, 1, time.Second}, {true, false, 1, time.Second}, {true, true, 0, time.Second}, {true, true, 17, time.Second}, {true, true, 1, time.Millisecond},
	} {
		d := NewPiKVM(PiKVMConfig{Network: NetworkPolicy{AllowLoopback: tc.access}, MappingVerified: tc.mapping})
		if g, err := d.WatchConsoleTarget(context.Background(), Target{Address: server.URL, Port: tc.port}, tc.interval); err == nil {
			g.Close()
			t.Fatal("unsafe guard accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("validation sent %d requests", calls.Load())
	}
}
