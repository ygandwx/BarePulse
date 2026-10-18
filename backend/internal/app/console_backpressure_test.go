package app

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// A pipe exercises real HTTP headers/chunk framing with deterministic transport
// backpressure: after headers the client stops reading, so Flush cannot finish.
type consolePipeListener struct {
	conn      net.Conn
	once      sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

func (l *consolePipeListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *consolePipeListener) Close() error   { l.closeOnce.Do(func() { close(l.closed) }); return nil }
func (l *consolePipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestConsoleRevocationInterruptsBlockedHTTPWrite(t *testing.T) {
	a := testApp(t)
	admin, _, _ := testUser(t, a, "admin")
	u, _, cookie := testUser(t, a, "customer")
	s, d := seedConsole(t, a, admin, u, "success")
	c := requestConsoleTest(t, a, u, s.ID)
	_, access := consoleGrant(t, a, u, c.ID)
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	l := &consolePipeListener{conn: serverConn, closed: make(chan struct{})}
	host := &http.Server{Handler: a.Handler()}
	defer host.Close()
	go host.Serve(l)
	if err := clientConn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := fmt.Fprintf(clientConn, "GET /api/v1/gateway/%s/events HTTP/1.1\r\nHost: localhost\r\nCookie: bp_session=%s\r\nX-Console-Token: %s\r\n\r\n", c.ID, cookie, access)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(clientConn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("stream HTTP %d", resp.StatusCode)
	}
	// Do not consume or close the response body before revocation. That would
	// unblock the transport and fail to exercise the slow-client condition.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var frames int64
		if err = a.DB.QueryRow(context.Background(), `SELECT frames FROM mock_console_state WHERE device_id=$1`, d.ID).Scan(&frames); err != nil {
			t.Fatal(err)
		}
		if frames > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not enter frame write")
		}
		time.Sleep(10 * time.Millisecond)
	}
	decodeTest[ConsoleSession](t, testRequest(t, a, "DELETE", "/api/v1/console-sessions/"+c.ID, nil, u), 202)
	deadline = time.Now().Add(2 * time.Second)
	for {
		var count int
		if err = a.DB.QueryRow(context.Background(), `SELECT count(*) FROM gateway_connections WHERE session_id=$1`, c.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked slow client retained its registered connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = a.ConsoleTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := decodeTest[ConsoleSession](t, testRequest(t, a, "GET", "/api/v1/console-sessions/"+c.ID, nil, u), 200)
	if got.State != "released" {
		t.Fatalf("cleanup did not finish: %+v", got)
	}
	a.streamMu.Lock()
	remaining := len(a.streams[c.ID])
	a.streamMu.Unlock()
	if remaining != 0 {
		t.Fatal("local connection cancellation registry leaked")
	}
}
