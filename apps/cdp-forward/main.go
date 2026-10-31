// Command cdp-forward publishes a loopback-only TCP listener on the pod IP.
//
// It exists because Chromium accepts --remote-debugging-address=0.0.0.0 and binds
// DevTools to 127.0.0.1 anyway, so nothing outside the pod's network namespace can
// reach it. This sidecar runs in the same namespace and splices bytes: the runner
// dials <podIP>:9224 and talks to Chromium's DevTools port as if it were local.
//
// It is a TCP splice, not a protocol proxy. It parses nothing, so an HTTP request,
// a WebSocket upgrade and a raw CDP frame all take the same path and arrive with
// their headers and frames intact.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type config struct {
	listen   string
	upstream string
	dialWait time.Duration
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.listen, "listen", envOr("CDP_FORWARD_LISTEN", ":9224"),
		"address to publish, reachable on the pod IP")
	flag.StringVar(&cfg.upstream, "upstream", envOr("CDP_FORWARD_UPSTREAM", "127.0.0.1:9223"),
		"loopback address Chromium listens on")
	flag.DurationVar(&cfg.dialWait, "dial-timeout", 5*time.Second, "how long to wait for the upstream")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, log.New(os.Stderr, "cdp-forward: ", log.LstdFlags|log.Lmicroseconds)); err != nil {
		log.Printf("cdp-forward: %v", err)
		os.Exit(1)
	}
}

// run listens until the context is cancelled. It is separated from main so a test
// can drive it without signals.
func run(ctx context.Context, cfg config, logger *log.Logger) error {
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return err
	}
	logger.Printf("publishing %s -> %s", cfg.listen, cfg.upstream)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	// Open connections are tracked so a shutdown cuts them: a pod being
	// terminated must not leave the runner waiting on a socket nobody will close.
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all = map[net.Conn]struct{}{}
	)
	closeAll := func() {
		mu.Lock()
		defer mu.Unlock()
		for conn := range all {
			conn.Close()
		}
	}
	go func() {
		<-ctx.Done()
		closeAll()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return err
		}
		wg.Add(1)
		mu.Lock()
		all[conn] = struct{}{}
		mu.Unlock()
		go func(client net.Conn) {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(all, client)
				mu.Unlock()
			}()
			splice(ctx, client, cfg, logger)
		}(conn)
	}
}

// splice connects one client to the upstream and copies both directions until
// either side is done. A half-close is propagated so a client that finishes
// sending still receives the rest of the response.
func splice(ctx context.Context, client net.Conn, cfg config, logger *log.Logger) {
	defer client.Close()

	dialer := net.Dialer{Timeout: cfg.dialWait}
	upstream, err := dialer.DialContext(ctx, "tcp", cfg.upstream)
	if err != nil {
		// Chromium may still be booting; the client sees a closed connection and
		// retries, which is what a browser restart looks like from outside.
		logger.Printf("dial %s: %v", cfg.upstream, err)
		return
	}
	defer upstream.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(upstream, client)
		halfClose(upstream)
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, upstream)
		halfClose(client)
	}()
	wg.Wait()
}

// halfClose shuts down the write side when the connection supports it, so the
// other direction keeps flowing.
func halfClose(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
		return
	}
	_ = conn.Close()
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
