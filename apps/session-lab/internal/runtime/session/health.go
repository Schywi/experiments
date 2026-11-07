package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
)

// cdpBridgePort is the port the in-pod shim publishes for HTTP callers. It is the
// port the SandboxTemplate readiness probe covers.
const cdpBridgePort = 9222

// probeTimeout bounds one health probe. A browser that cannot answer /json/version
// inside this window is not going to serve a session, and the session's own
// deadline is the wrong place to discover that.
const probeTimeout = 300 * time.Millisecond

// probeAddress is the CDP endpoint this run actually uses: the forwarder sidecar
// for the ws transport, the bridge otherwise. Probing the port the run does not
// use is how a dead forwarder stayed invisible while the pod reported Ready.
func (r *Runner) probeAddress(instance *sandbox.Instance) string {
	port := cdpBridgePort
	if r.cfg.CDPTransport == "ws" {
		port = r.cdpPort()
	}
	return fmt.Sprintf("%s:%d", instance.PodIP, port)
}

// probe asks a browser whether it can still serve work, by reading
// /json/version. Any transport error or non-200 answer means it cannot.
func probeCDP(ctx context.Context, address string, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/json/version", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: %s", address, response.Status)
	}
	return nil
}

// probeHTTPClient is shared by every probe in a run: one connection pool for
// /json/version instead of a client per assignment.
var probeHTTPClient = &http.Client{}

// probeInstance is the health check the slot pool calls before it hands out a
// slot. A pod without an IP has not bound yet, so it is not probed.
func (r *Runner) probeInstance(ctx context.Context, instance *sandbox.Instance) error {
	if instance == nil {
		return fmt.Errorf("probe: no instance")
	}
	if instance.PodIP == "" {
		return fmt.Errorf("probe %s: no pod IP", instance.ID)
	}
	return probeCDP(ctx, r.probeAddress(instance), probeHTTPClient)
}
