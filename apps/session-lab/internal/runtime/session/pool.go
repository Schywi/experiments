package session

import (
	"context"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/infra/sandbox"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/schedule"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/slots"
)

// poolFor resolves the warm pool one session is routed to. Precedence:
// a per-session pool from the plan, then the class mapping, then the run's
// single default pool. A session with no plan entry and no class mapping keeps
// today's behaviour: it lands in r.cfg.Pool.
func (r *Runner) poolFor(planned schedule.Planned) string {
	if planned.Pool != "" {
		return planned.Pool
	}
	return r.poolForClass(r.cfg.Class)
}

// poolForClass is the same resolution for call sites that have a class but no
// plan entry. An empty class is the normal class: it is what the operator gets
// by default, and it must match a "normal" mapping key.
func (r *Runner) poolForClass(class model.Class) string {
	if class == "" {
		class = model.ClassNormal
	}
	if r.cfg.PoolByClass != nil {
		if pool, ok := r.cfg.PoolByClass[string(class)]; ok && pool != "" {
			return pool
		}
	}
	return r.cfg.Pool
}

// poolNames is the set of warm pools a run can draw from: the default pool plus
// every class-mapped pool, in that order and without duplicates.
func (r *Runner) poolNames() []string {
	names := []string{r.cfg.Pool}
	seen := map[string]bool{r.cfg.Pool: true}
	for _, pool := range r.cfg.PoolByClass {
		if pool == "" || seen[pool] {
			continue
		}
		seen[pool] = true
		names = append(names, pool)
	}
	return names
}

// spoolFor returns the slot pool for one warm pool, creating it on first use. It
// is nil-safe only when the run uses the one-session-per-instance path.
func (r *Runner) spoolFor(pool string) *slots.Pool {
	if r.spools == nil {
		return nil
	}
	if spool, ok := r.spools[pool]; ok {
		return spool
	}
	spool := slots.New(r.cfg.ContextsPerBrowser, func(ctx context.Context) (*sandbox.Instance, error) {
		return r.acquireInstance(ctx, pool)
	})
	r.spools[pool] = spool
	return spool
}

// retireEverywhere drops a dead browser from every slot pool that may hold it.
func (r *Runner) retireEverywhere(instance *sandbox.Instance) {
	if r.spools == nil {
		return
	}
	for _, spool := range r.spools {
		spool.Retire(instance)
	}
}

// classKey is the class as a stable storage key: the empty class is the normal
// class, so peaks recorded with no class match a run that asked for normal.
func classKey(class model.Class) string {
	if class == "" {
		return string(model.ClassNormal)
	}
	return string(class)
}
