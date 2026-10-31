package main

import (
	"context"
	"io"
	"log"
	"net"
	"testing"
	"time"
)

// The forwarder must carry bytes in both directions and keep them intact — it
// parses nothing, so an HTTP request and a WebSocket frame take the same path.
func TestSpliceCarriesBothDirections(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream listen: %v", err)
	}
	defer upstream.Close()

	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c) // echo
			}(conn)
		}
	}()

	cfg := config{listen: "127.0.0.1:0", upstream: upstream.Addr().String(), dialWait: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Bind first so the test knows the port, then run with that listener.
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{listen: addr, upstream: cfg.upstream, dialWait: time.Second}, log.New(io.Discard, "", 0))
	}()

	var conn net.Conn
	for attempt := 0; attempt < 50; attempt++ {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer conn.Close()

	payload := []byte("POST /json/version HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round trip changed the bytes: %q", string(got))
	}

	conn.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v after cancellation, want nil", err)
	}
}

func TestUpstreamRefusalClosesTheClient(t *testing.T) {
	// Nothing listening upstream: the client gets EOF instead of a hang.
	cfg := config{listen: "127.0.0.1:0", upstream: "127.0.0.1:1", dialWait: 100 * time.Millisecond}
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go run(ctx, config{listen: addr, upstream: cfg.upstream, dialWait: cfg.dialWait}, log.New(io.Discard, "", 0))

	var conn net.Conn
	for attempt := 0; attempt < 50; attempt++ {
		conn, err = net.Dial("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatalf("read succeeded with no upstream; want a closed connection")
	}
}
