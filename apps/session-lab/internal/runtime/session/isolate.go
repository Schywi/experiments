package session

// Session isolation has two transports. The HTTP one goes through the in-pod
// bridge (one command per call, no events); the WebSocket one talks to Chromium
// directly and keeps a real CDP session per browser context, which is what makes
// events and per-context metrics available.

import (
	"context"
	"fmt"
	"strings"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// isolated is one session's own browsing state: a context, and a page inside it.
type isolated struct {
	context browser.ContextID
	target  browser.TargetID
	session *browser.Session
	conn    *browser.Conn
	viaHTTP bool
	before  browser.Usage
}

// openIsolated gives a session its own state on an instance, using whichever
// transport the run was configured with.
func (r *Runner) openIsolated(ctx context.Context, instance *sandbox.Instance) (isolated, error) {
	if r.cfg.CDPTransport != "ws" {
		session, err := r.ad.OpenSession(ctx, instance, r.cfg.TargetURL)
		if err != nil {
			return isolated{}, err
		}
		return isolated{context: browser.ContextID(session.ContextID), target: browser.TargetID(session.TargetID), viaHTTP: true}, nil
	}

	conn, err := r.browserConn(ctx, instance)
	if err != nil {
		return isolated{}, err
	}
	contextID, err := conn.NewContext(ctx)
	if err != nil {
		return isolated{}, err
	}
	targetID, err := conn.NewTarget(ctx, r.targetURL(), contextID)
	if err != nil {
		// Leave nothing behind: a context outlives a failed page creation.
		_ = conn.DisposeContext(ctx, contextID)
		return isolated{}, err
	}
	session, err := conn.Attach(ctx, targetID)
	if err != nil {
		_ = conn.CloseTarget(ctx, targetID)
		_ = conn.DisposeContext(ctx, contextID)
		return isolated{}, err
	}
	opened := isolated{context: contextID, target: targetID, session: session, conn: conn}
	// Page events (load) and Performance counters are only emitted for a session
	// that enabled their domains.
	if err := session.EnablePage(ctx); err != nil {
		r.closeIsolated(ctx, instance, opened)
		return isolated{}, fmt.Errorf("enable page: %w", err)
	}
	if err := session.EnableMetrics(ctx); err != nil {
		r.closeIsolated(ctx, instance, opened)
		return isolated{}, fmt.Errorf("enable metrics: %w", err)
	}
	usage, err := session.Metrics(ctx)
	if err != nil {
		r.closeIsolated(ctx, instance, opened)
		return isolated{}, fmt.Errorf("metrics: %w", err)
	}
	opened.before = usage
	return opened, nil
}

// closeIsolated releases the session's state and reports what it cost, when the
// transport can measure that.
func (r *Runner) closeIsolated(ctx context.Context, instance *sandbox.Instance, opened isolated) error {
	if opened.viaHTTP {
		return r.ad.CloseSession(ctx, instance, sandbox.BrowserSession{
			InstanceID: instance.ID,
			ContextID:  string(opened.context),
			TargetID:   string(opened.target),
		})
	}

	var firstErr error
	if opened.session != nil {
		usage, err := opened.session.Metrics(ctx)
		if err == nil {
			delta := usage.Delta(opened.before)
			r.log.Printf("session cpu (task) %.0fms script %.0fms heap %.0fKiB on %s",
				delta.TaskDurationSeconds*1000, delta.ScriptDurationSeconds*1000,
				delta.JSHeapUsedBytes/1024, instance.ID)
		}
		if err := opened.session.Close(ctx); err != nil {
			firstErr = err
		}
	}
	if err := opened.conn.CloseTarget(ctx, opened.target); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := opened.conn.DisposeContext(ctx, opened.context); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// browserConn returns the run's connection to this instance's Chromium, dialling
// it the first time. One connection serves every context in that browser.
func (r *Runner) browserConn(ctx context.Context, instance *sandbox.Instance) (*browser.Conn, error) {
	r.mu.Lock()
	if conn, ok := r.browsers[instance.ID]; ok {
		r.mu.Unlock()
		return conn, nil
	}
	r.mu.Unlock()

	if instance.PodIP == "" {
		return nil, fmt.Errorf("browser %s: no pod IP", instance.ID)
	}
	address := fmt.Sprintf("%s:%d", instance.PodIP, r.cdpPort())
	conn, err := browser.Open(ctx, address, nil)
	if err != nil {
		return nil, fmt.Errorf("browser %s: %w", instance.ID, err)
	}
	r.mu.Lock()
	if existing, ok := r.browsers[instance.ID]; ok {
		r.mu.Unlock()
		conn.Close()
		return existing, nil
	}
	r.browsers[instance.ID] = conn
	r.mu.Unlock()

	// A renderer crash or a closed socket invalidates every context in this
	// browser, so the instance must stop being handed out.
	go r.watchBrowser(conn, instance)
	return conn, nil
}

// watchBrowser retires an instance when its browser goes away.
func (r *Runner) watchBrowser(conn *browser.Conn, instance *sandbox.Instance) {
	for crash := range conn.WatchCrashes(context.Background()) {
		r.log.Printf("browser %s crashed a renderer: target %s status %s code %d",
			instance.ID, crash.TargetID, crash.Status, crash.Code)
		if r.spool != nil {
			r.spool.Retire(instance)
		}
	}
	if err := conn.Err(); err != nil {
		r.log.Printf("browser %s closed: %v", instance.ID, err)
		if r.spool != nil {
			r.spool.Retire(instance)
		}
	}
}

// forgetBrowser drops the cached connection for an instance that is being deleted.
func (r *Runner) forgetBrowser(instance *sandbox.Instance) {
	r.mu.Lock()
	conn, ok := r.browsers[instance.ID]
	delete(r.browsers, instance.ID)
	r.mu.Unlock()
	if ok {
		conn.Close()
	}
}

func (r *Runner) cdpPort() int {
	if r.cfg.CDPPort > 0 {
		return r.cfg.CDPPort
	}
	return 9224
}

// targetURL is the page a session opens. A shared browser starts every session on
// a blank page unless the operator named one.
func (r *Runner) targetURL() string {
	if strings.TrimSpace(r.cfg.TargetURL) == "" {
		return "about:blank"
	}
	return r.cfg.TargetURL
}

var _ = model.PolicyReuse
