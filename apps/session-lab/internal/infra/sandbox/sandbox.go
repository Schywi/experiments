// Package adapter is the runner's view of the sandbox platform.
//
// One interface, one Kubernetes implementation, and a fake for tests. The
// adapter owns instance lifecycle calls only; session state belongs to the
// scheduler in internal/run.
package sandbox

import (
	"context"
	"fmt"
	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/model"
	"sync"
	"time"
)

// Kind is how an instance was obtained.
type Kind string

const (
	// KindClaim adopts a warm-pool sandbox through a SandboxClaim.
	KindClaim Kind = "claim"
)

// Instance is one sandbox the runner is holding.
type Instance struct {
	ID        string
	Kind      Kind
	Namespace string
	Claim     string
	Sandbox   string
	Node      string
	PodUID    string
	PodIP     string
	Warm      bool
	CreatedNS int64
	ReadyNS   int64
	DeletedNS int64
	Suspends  int64
	Resumes   int64
	Resets    int64
	Sessions  int64
	Suspended bool
}

// BrowserSession is one session's own browsing context inside a Chromium. It
// separates cookies, storage and cache from every other session in the same
// browser, and shares that browser's process: a crash takes all of them.
type BrowserSession struct {
	InstanceID string
	ContextID  string
	TargetID   string
}

// PoolState is what the warm pool reports right now.
type PoolState struct {
	Name     string
	Desired  int32
	Ready    int32
	Template string
	// Paused is true when the pool carries the annotation that tells an
	// autoscaler to leave its replica count alone.
	Paused bool
}

// AnnotationAutoscalerPaused marks a pool whose replica count an experiment owns.
const AnnotationAutoscalerPaused = "session-lab.io/autoscaler"

// NodeCapacity is the node's allocatable budget, read from the API.
type NodeCapacity struct {
	Name     string
	CPUMilli int64
	MemBytes int64
	Pods     int64
}

// TemplateLimits is what a sandbox pod is held to. It is recorded with the run
// so a throttled session carries the limit that throttled it.
type TemplateLimits struct {
	Container       string `json:"container"`
	CPURequestMilli int64  `json:"cpu_request_milli"`
	CPULimitMilli   int64  `json:"cpu_limit_milli"`
	MemRequestBytes int64  `json:"mem_request_bytes"`
	MemLimitBytes   int64  `json:"mem_limit_bytes"`
}

// ClaimStats is the demand signal a pool autoscaler needs: claims waiting for an
// instance and claims that already hold one.
type ClaimStats struct {
	Pending int32
	Ready   int32
	// Failed counts claims the platform gave up on. They are not demand: no
	// amount of extra replicas will make them Ready.
	Failed int32
	// Bulk counts claims whose class never grows the pool. They hold capacity
	// but do not create demand for it.
	Bulk int32
}

// Boundary is one observation that the platform refused or degraded something:
// an OOM kill, an eviction, a scheduling refusal.
type Boundary struct {
	At      time.Time
	Kind    string
	Object  string
	Message string
}

// Adapter is the whole surface the runner needs from the cluster.
type Adapter interface {
	// Acquire creates a claim against the pool and returns as soon as the
	// object exists; WaitReady does the waiting.
	Acquire(ctx context.Context, namespace, pool, name string, class model.Class) (*Instance, error)
	// WaitReady blocks until the instance reports Ready and fills Node, PodUID
	// and PodIP from what the platform reports.
	WaitReady(ctx context.Context, instance *Instance) error
	// OpenSession creates an isolated browsing context for one session inside a
	// shared Chromium, and returns the identifiers needed to close it.
	OpenSession(ctx context.Context, instance *Instance, url string) (BrowserSession, error)
	// CloseSession closes the session's page and disposes its context.
	CloseSession(ctx context.Context, instance *Instance, session BrowserSession) error
	// Reset returns the browser to a blank page so the next session starts
	// from a known state.
	Reset(ctx context.Context, instance *Instance) error
	// Suspend terminates the backing pod while keeping the sandbox object.
	Suspend(ctx context.Context, instance *Instance) error
	// Resume brings a suspended instance back.
	Resume(ctx context.Context, instance *Instance) error
	// Release deletes the instance and its claim.
	Release(ctx context.Context, instance *Instance) error
	// ClaimStats counts the claims that are waiting and the claims that hold an
	// instance, for the given pool.
	ClaimStats(ctx context.Context, namespace, pool string) (ClaimStats, error)
	// Pool reports the warm pool's desired and ready counts.
	Pool(ctx context.Context, namespace, name string) (PoolState, error)
	// SetPoolReplicas writes the pool's replica count.
	// SetPoolPaused puts the pool under experiment ownership or releases it.
	SetPoolPaused(ctx context.Context, namespace, name string, paused bool) error
	SetPoolReplicas(ctx context.Context, namespace, name string, replicas int32) error
	// Template reads the requests and limits the sandbox template applies.
	Template(ctx context.Context, namespace, name string) (TemplateLimits, error)
	// Node reads the allocatable budget of the node the sandboxes run on.
	Node(ctx context.Context, namespace string) (NodeCapacity, error)
	// Boundaries returns refusals and degradations observed since the given
	// time, from the events stream.
	Boundaries(ctx context.Context, namespace string, since time.Time) ([]Boundary, error)
}

// Fake is an in-memory adapter for tests and dry runs. It models latency,
// adoption and refusals without a cluster.
type Fake struct {
	mu sync.Mutex

	// AcquireLatency and ReadyLatency shape each acquisition.
	AcquireLatency time.Duration
	ReadyLatency   time.Duration
	ResetLatency   time.Duration
	ReleaseLatency time.Duration

	// AcquireErrors are returned by the next acquisitions, one per call, so a
	// test can model a control plane that fails once and then recovers.
	AcquireErrors []error

	// FailAcquire, FailReset, FailRelease make the corresponding call fail.
	FailAcquire bool
	FailReset   bool
	FailRelease bool

	// OpenSessionErr and CloseSessionErr fail the browser-context calls.
	OpenSessionErr  error
	CloseSessionErr error
	sessionSeq      int

	// ReadyAfter is how many acquisitions adopt a warm instance; later ones
	// cold-start. Zero means every acquisition is warm.
	WarmInstances int

	// RefuseAfter makes Acquire fail once this many instances are live, which
	// is how a capacity boundary is modelled without a cluster.
	RefuseAfter int

	acquired int
	live     int
	nextID   int
	pool     PoolState
	node     NodeCapacity
	// Limits is what Template reports.
	Limits TemplateLimits
	// Stats is what ClaimStats reports.
	Stats ClaimStats
}

// NewFake returns a fake adapter.
func NewFake() *Fake {
	return &Fake{pool: PoolState{Name: "browser-pool", Desired: 1, Ready: 1}, node: NodeCapacity{Name: "fake"}}
}

// Acquire implements Adapter.
func (f *Fake) Acquire(ctx context.Context, namespace, pool, name string, class model.Class) (*Instance, error) {
	f.mu.Lock()
	if len(f.AcquireErrors) > 0 {
		err := f.AcquireErrors[0]
		f.AcquireErrors = f.AcquireErrors[1:]
		f.mu.Unlock()
		return nil, err
	}
	f.mu.Unlock()
	if err := sleep(ctx, f.AcquireLatency); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailAcquire {
		return nil, fmt.Errorf("fake: acquire refused")
	}
	if f.RefuseAfter > 0 && f.live >= f.RefuseAfter {
		return nil, fmt.Errorf("fake: %d instances live, refusing", f.live)
	}
	f.acquired++
	f.nextID++
	f.live++
	warm := f.WarmInstances == 0 || f.acquired <= f.WarmInstances
	return &Instance{
		ID:        fmt.Sprintf("inst-%d", f.nextID),
		Kind:      KindClaim,
		Namespace: namespace,
		Claim:     name,
		Warm:      warm,
		CreatedNS: time.Now().UnixNano(),
	}, nil
}

// WaitReady implements Adapter.
func (f *Fake) WaitReady(ctx context.Context, instance *Instance) error {
	latency := f.ReadyLatency
	if !instance.Warm {
		latency *= 3
	}
	if err := sleep(ctx, latency); err != nil {
		return err
	}
	instance.ReadyNS = time.Now().UnixNano()
	instance.Node = "fake"
	instance.PodUID = instance.ID + "-pod"
	instance.PodIP = "127.0.0.1"
	return nil
}

// OpenSession implements Adapter.
func (f *Fake) OpenSession(ctx context.Context, instance *Instance, url string) (BrowserSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.OpenSessionErr != nil {
		return BrowserSession{}, f.OpenSessionErr
	}
	f.sessionSeq++
	instance.Sessions++
	return BrowserSession{
		InstanceID: instance.ID,
		ContextID:  fmt.Sprintf("ctx-%d", f.sessionSeq),
		TargetID:   fmt.Sprintf("target-%d", f.sessionSeq),
	}, nil
}

// CloseSession implements Adapter.
func (f *Fake) CloseSession(ctx context.Context, instance *Instance, session BrowserSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CloseSessionErr != nil {
		return f.CloseSessionErr
	}
	return nil
}

// Reset implements Adapter.
func (f *Fake) Reset(ctx context.Context, instance *Instance) error {
	if err := sleep(ctx, f.ResetLatency); err != nil {
		return err
	}
	if f.FailReset {
		return fmt.Errorf("fake: reset failed")
	}
	instance.Resets++
	return nil
}

// Suspend implements Adapter.
func (f *Fake) Suspend(ctx context.Context, instance *Instance) error {
	instance.Suspends++
	instance.Suspended = true
	return nil
}

// Resume implements Adapter.
func (f *Fake) Resume(ctx context.Context, instance *Instance) error {
	instance.Resumes++
	instance.Suspended = false
	return nil
}

// Release implements Adapter.
func (f *Fake) Release(ctx context.Context, instance *Instance) error {
	if err := sleep(ctx, f.ReleaseLatency); err != nil {
		return err
	}
	if f.FailRelease {
		return fmt.Errorf("fake: release failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live--
	instance.DeletedNS = time.Now().UnixNano()
	return nil
}

// Pool implements Adapter.
func (f *Fake) Pool(ctx context.Context, namespace, name string) (PoolState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.pool
	state.Name = name
	// The fake pool fills instantly, so a preflight that waits for
	// ready == desired returns instead of blocking the run.
	state.Ready = int32(f.live)
	if state.Ready < state.Desired {
		state.Ready = state.Desired
	}
	return state, nil
}

// SetPoolReplicas implements Adapter.
func (f *Fake) SetPoolReplicas(ctx context.Context, namespace, name string, replicas int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pool.Desired = replicas
	return nil
}

// SetPoolPaused implements Adapter.
func (f *Fake) SetPoolPaused(ctx context.Context, namespace, name string, paused bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pool.Paused = paused
	return nil
}

// ClaimStats implements Adapter.
func (f *Fake) ClaimStats(ctx context.Context, namespace, pool string) (ClaimStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Stats, nil
}

// Template implements Adapter.
func (f *Fake) Template(ctx context.Context, namespace, name string) (TemplateLimits, error) {
	limits := f.Limits
	if limits.Container == "" {
		limits.Container = name
	}
	return limits, nil
}

// Node implements Adapter.
func (f *Fake) Node(ctx context.Context, namespace string) (NodeCapacity, error) {
	return f.node, nil
}

// Boundaries implements Adapter.
func (f *Fake) Boundaries(ctx context.Context, namespace string, since time.Time) ([]Boundary, error) {
	return nil, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var _ Adapter = (*Fake)(nil)
