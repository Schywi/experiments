package kube

import (
	"context"
	"fmt"
	sandbox "github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

// claimObservation is what a claim looks like for counting purposes: whether it
// holds an instance, whether the platform gave up on it, and which pool it asked
// for. Kept small so the cache stays cheap with hundreds of claims.
type claimObservation struct {
	ready  bool
	failed bool
	pool   string
	// bulk marks a claim whose class never grows the pool. It holds capacity
	// without creating demand for more.
	bulk bool
}

// observeClaim folds one claim object into an observation. Every watch event and
// every initial list item goes through here, so the counting rules exist once.
func observeClaim(claim *unstructured.Unstructured) (string, claimObservation) {
	var obs claimObservation
	obs.pool, _, _ = unstructured.NestedString(claim.Object, "spec", "warmPoolRef", "name")
	if class, ok, _ := unstructured.NestedString(claim.Object, "metadata", "labels", labelClass); ok {
		obs.bulk = model.Class(class) == model.ClassBulk
	}
	obs.ready, _ = claimState(claim)
	if !obs.ready {
		obs.failed = claimFailed(claim)
	}
	return claim.GetName(), obs
}

// countObservations turns the cache into the counts the autoscaler decides on.
func countObservations(cache map[string]claimObservation, pool string) sandbox.ClaimStats {
	var stats sandbox.ClaimStats
	for _, obs := range cache {
		if pool != "" && obs.pool != "" && obs.pool != pool {
			continue
		}
		switch {
		case obs.bulk:
			stats.Bulk++
		case obs.ready:
			stats.Ready++
		case obs.failed:
			stats.Failed++
		default:
			stats.Pending++
		}
	}
	return stats
}

// applyClaimEvent updates the cache from one watch event.
func applyClaimEvent(cache map[string]claimObservation, event watch.Event) {
	claim, ok := event.Object.(*unstructured.Unstructured)
	if !ok {
		return
	}
	name := claim.GetName()
	switch event.Type {
	case watch.Added, watch.Modified:
		_, cache[name] = observeClaim(claim)
	case watch.Deleted:
		delete(cache, name)
	}
}

// markReady records the transition and reads the sandbox the claim adopted.
func (k *Kube) markReady(ctx context.Context, instance *sandbox.Instance, sandbox string) error {
	instance.ReadyNS = time.Now().UnixNano()
	if sandbox == "" {
		return nil
	}
	instance.Sandbox = sandbox
	filled, err := k.fillFromSandbox(ctx, instance)
	if err != nil {
		return err
	}
	_ = filled
	return nil
}

// waitClaimReady blocks until the claim reports Ready, driven by a watch on that
// one claim instead of a Get every second. It starts with a direct read so a
// claim that is already Ready costs one request, and it re-lists whenever the
// watch dies or the resource version expires (410).
func (k *Kube) waitClaimReady(ctx context.Context, instance *sandbox.Instance) error {
	var lastErr error
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 10; attempt++ {
		claim, err := k.dyn.Resource(gvrClaims).Namespace(instance.Namespace).
			Get(ctx, instance.Claim, metav1.GetOptions{})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("get claim %s/%s: %w", instance.Namespace, instance.Claim, err)
		} else if ready, sandbox := claimState(claim); ready {
			return k.markReady(ctx, instance, sandbox)
		}

		// No ResourceVersionMatch here: the API server rejects it on a watch
		// ("forbidden unless sendInitialEvents is provided"), and a rejected watch
		// turns every wait into ten retries with backoff. That is how a 300s run
		// took eleven minutes with acquisition p95 at 266s.
		options := metav1.ListOptions{
			FieldSelector:       "metadata.name=" + instance.Claim,
			AllowWatchBookmarks: true,
		}
		if claim != nil {
			options.ResourceVersion = claim.GetResourceVersion()
		}
		watcher, err := k.dyn.Resource(gvrClaims).Namespace(instance.Namespace).Watch(ctx, options)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = fmt.Errorf("watch claim %s/%s: %w", instance.Namespace, instance.Claim, err)
			if !sleepCtx(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff)
			continue
		}

		ready, sandbox, stopped := consumeClaimWatch(ctx, watcher)
		watcher.Stop()
		if ready {
			return k.markReady(ctx, instance, sandbox)
		}
		if stopped {
			return ctx.Err()
		}
		// The watch ended without the transition: re-read and watch again from
		// the claim's current version.
		backoff = 200 * time.Millisecond
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("claim %s/%s never reported Ready", instance.Namespace, instance.Claim)
}

// consumeClaimWatch reads events until the claim turns Ready, the watch ends, or
// the context is done. stopped reports a cancelled context so the caller does
// not mistake it for a lapsed watch.
func consumeClaimWatch(ctx context.Context, watcher watch.Interface) (ready bool, sandbox string, stopped bool) {
	for {
		select {
		case <-ctx.Done():
			return false, "", true
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return false, "", false
			}
			if event.Type == watch.Error {
				return false, "", false
			}
			claim, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if isReady, sandboxName := claimState(claim); isReady {
				return true, sandboxName, false
			}
		}
	}
}

// claimCacheLoop keeps a map of claims current from one watch, re-listing after
// any interruption. It runs for the life of the process: the autoscaler asks for
// counts every few seconds and must not pay a List each time.
func (k *Kube) claimCacheLoop(namespace string) {
	backoff := time.Second
	for {
		list, err := k.dyn.Resource(gvrClaims).Namespace(namespace).
			List(context.Background(), metav1.ListOptions{})
		if err != nil {
			k.setClaimCacheError(err)
			time.Sleep(backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		cache := make(map[string]claimObservation, len(list.Items))
		for i := range list.Items {
			name, obs := observeClaim(&list.Items[i])
			cache[name] = obs
		}
		k.setClaimCache(cache)

		watcher, err := k.dyn.Resource(gvrClaims).Namespace(namespace).Watch(
			context.Background(),
			metav1.ListOptions{ResourceVersion: list.GetResourceVersion(), AllowWatchBookmarks: true},
		)
		if err != nil {
			k.setClaimCacheError(err)
			time.Sleep(backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
		k.setClaimCacheError(nil)

		for event := range watcher.ResultChan() {
			if event.Type == watch.Error {
				break
			}
			k.applyClaimCacheEvent(event)
		}
		watcher.Stop()
	}
}

// nextBackoff doubles a delay up to thirty seconds.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > 30*time.Second {
		return 30 * time.Second
	}
	return next
}

// sleepCtx waits for a delay or for cancellation, reporting whether it slept.
func sleepCtx(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
