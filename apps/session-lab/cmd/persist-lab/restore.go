package main

// The restore arm: after the load run has finished, take one browser and boot a
// session from the blobs the run stored. This is the claim the experiment
// exists to answer — a stored blob can be turned back into a live browser
// context — and it is why the arm runs on a fresh claim rather than on the
// browser that wrote the blobs.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/kube"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/persist"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// restoreResult is what the restore arm did, written beside the run's storage
// stats.
type restoreResult struct {
	Selected      int     `json:"selected"`
	OK            int     `json:"ok"`
	Failed        int     `json:"failed"`
	EgressBytes   int64   `json:"egress_bytes"`
	CookiesBack   int     `json:"cookies_restored"`
	CookiesWanted int     `json:"cookies_stored"`
	ItemsBack     int     `json:"items_restored"`
	ItemsWanted   int     `json:"items_stored"`
	Seconds       float64 `json:"seconds"`
	MeanSeconds   float64 `json:"mean_seconds"`
}

// restoreStored boots one session per selected blob on a fresh claim.
//
// rate selects blobs with a fixed stride — at 0.2 every fifth blob — so two runs
// at one rate restore the same ones.
func restoreStored(ctx context.Context, adapter *kube.Kube, cfg model.RunConfig,
	store persist.Store, rate float64, log *log.Logger) (restoreResult, error) {
	var result restoreResult
	objects := store.Objects()
	if len(objects) == 0 {
		return result, nil
	}
	stride := int(1 / rate)
	if stride < 1 {
		stride = 1
	}
	selected := make([]persist.Object, 0, len(objects)/stride+1)
	for i := 0; i < len(objects); i += stride {
		selected = append(selected, objects[i])
	}
	result.Selected = len(selected)

	name := fmt.Sprintf("persist-rs-%d", time.Now().Unix()%100000)
	instance, err := adapter.Acquire(ctx, cfg.Namespace, cfg.Pool, name, model.ClassNormal)
	if err != nil {
		return result, fmt.Errorf("restore claim: %w", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := adapter.Release(releaseCtx, instance); err != nil {
			log.Printf("restore: release %s: %v", instance.ID, err)
		}
	}()
	if err := adapter.WaitReady(ctx, instance); err != nil {
		return result, fmt.Errorf("restore claim ready: %w", err)
	}

	port := cfg.CDPPort
	if port == 0 {
		port = 9224
	}
	conn, err := browser.Open(ctx, fmt.Sprintf("%s:%d", instance.PodIP, port), nil)
	if err != nil {
		return result, fmt.Errorf("restore browser: %w", err)
	}
	defer conn.Close()

	started := time.Now()
	for _, object := range selected {
		blob, err := store.Get(ctx, object.Key)
		if err != nil {
			log.Printf("restore: %s: %v", object.Key, err)
			result.Failed++
			continue
		}
		result.EgressBytes += object.Bytes
		result.CookiesWanted += len(blob.Cookies)
		result.ItemsWanted += blob.Items()

		cookies, items, err := restoreOne(ctx, conn, blob)
		if err != nil {
			log.Printf("restore: %s: %v", object.Key, err)
			result.Failed++
			continue
		}
		result.CookiesBack += cookies
		result.ItemsBack += items
		result.OK++
	}
	result.Seconds = time.Since(started).Seconds()
	if result.OK > 0 {
		result.MeanSeconds = result.Seconds / float64(result.OK)
	}
	return result, nil
}

// restoreOne creates a context, writes the blob into it and reads the state back
// out of the restored context.
func restoreOne(ctx context.Context, conn *browser.Conn, blob persist.Blob) (int, int, error) {
	contextID, err := conn.NewContext(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = conn.DisposeContext(context.Background(), contextID) }()

	target, err := conn.NewTarget(ctx, "about:blank", contextID)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = conn.CloseTarget(context.Background(), target) }()

	session, err := conn.Attach(ctx, target)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = session.Close(context.Background()) }()

	// Page.enable first: Chromium emits Page.loadEventFired only for a session
	// that enabled the domain, and Restore waits for that event after its
	// navigation. Without this every restore of a blob with localStorage dies on
	// "wait for load: no loadEventFired" after the full timeout.
	if err := session.EnablePage(ctx); err != nil {
		return 0, 0, fmt.Errorf("enable page: %w", err)
	}

	if err := persist.Restore(ctx, conn, session, contextID, blob); err != nil {
		return 0, 0, err
	}
	return persist.Verify(ctx, conn, session, contextID, blob)
}

// String is the one line the run prints about the restore arm.
func (r restoreResult) String() string {
	return fmt.Sprintf("restore: %d of %d selected, ok %d, failed %d, egress %d bytes, mean %.0f ms",
		r.OK, r.Selected, r.OK, r.Failed, r.EgressBytes, r.MeanSeconds*1000)
}
