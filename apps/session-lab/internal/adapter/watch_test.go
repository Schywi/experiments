package adapter

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

func claimObject(name, pool string, ready bool, reason string) *unstructured.Unstructured {
	claim := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"spec":     map[string]any{"warmPoolRef": map[string]any{"name": pool}},
	}}
	if ready {
		claim.Object["status"] = map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
			"sandbox":    map[string]any{"name": "sb-" + name},
		}
		return claim
	}
	if reason != "" {
		claim.Object["status"] = map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": reason}},
		}
	}
	return claim
}

func TestClaimCacheCountsFromEventsOnly(t *testing.T) {
	// The autoscaler's counts come from watch events; nothing here asks the API
	// server for a list.
	cache := map[string]claimObservation{}
	for _, claim := range []*unstructured.Unstructured{
		claimObject("held-1", "browser-pool", true, ""),
		claimObject("held-2", "browser-pool", true, ""),
		claimObject("waiting-1", "browser-pool", false, ""),
		claimObject("dead-1", "browser-pool", false, "PodFailed"),
		claimObject("other-1", "other-pool", false, ""),
	} {
		applyClaimEvent(cache, watch.Event{Type: watch.Added, Object: claim})
	}

	stats := countObservations(cache, "browser-pool")
	if stats.Ready != 2 || stats.Pending != 1 || stats.Failed != 1 {
		t.Fatalf("stats = %+v, want ready 2 pending 1 failed 1", stats)
	}

	// A claim that goes Ready moves out of pending without a second event type.
	applyClaimEvent(cache, watch.Event{Type: watch.Modified, Object: claimObject("waiting-1", "browser-pool", true, "")})
	if got := countObservations(cache, "browser-pool"); got.Ready != 3 || got.Pending != 0 {
		t.Fatalf("after adoption: %+v, want ready 3 pending 0", got)
	}

	// Deletion removes it from demand entirely.
	applyClaimEvent(cache, watch.Event{Type: watch.Deleted, Object: claimObject("held-1", "browser-pool", true, "")})
	if got := countObservations(cache, "browser-pool"); got.Ready != 2 {
		t.Fatalf("after delete: %+v, want ready 2", got)
	}
}

func TestWatchReturnsOnTheReadyTransition(t *testing.T) {
	fake := watch.NewFake()
	ready := make(chan struct{})

	go func() {
		// A claim that changes twice before it holds an instance: the loop must
		// keep reading, not return on the first event.
		fake.Modify(claimObject("c1", "browser-pool", false, ""))
		fake.Modify(claimObject("c1", "browser-pool", false, ""))
		fake.Modify(claimObject("c1", "browser-pool", true, ""))
		close(ready)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	isReady, sandbox, stopped := consumeClaimWatch(ctx, fake)
	if stopped {
		t.Fatalf("watch reported a cancelled context")
	}
	if !isReady {
		t.Fatalf("watch did not report the Ready transition")
	}
	if sandbox != "sb-c1" {
		t.Fatalf("sandbox = %q, want sb-c1", sandbox)
	}
	<-ready
}

func TestWatchStopsOnCancellation(t *testing.T) {
	fake := watch.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, _, stopped := consumeClaimWatch(ctx, fake); !stopped {
		t.Fatalf("cancellation was not reported, the caller would treat it as a lapsed watch")
	}
}

func TestWatchEndsWhenTheServerCloses(t *testing.T) {
	fake := watch.NewFake()
	go func() {
		fake.Modify(claimObject("c1", "browser-pool", false, ""))
		fake.Stop()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, stopped := consumeClaimWatch(ctx, fake); stopped {
		t.Fatalf("a closed watch was reported as a cancelled context")
	}
}

func TestExpiredVersionIsHandledByReReading(t *testing.T) {
	// watch.Error is how the server says the resource version is gone; the loop
	// must fall back to a fresh read instead of blocking forever.
	fake := watch.NewFake()
	go func() {
		fake.Error(&metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonExpired, Code: 410})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, stopped := consumeClaimWatch(ctx, fake); stopped {
		t.Fatalf("a 410 was reported as cancellation")
	}
}
