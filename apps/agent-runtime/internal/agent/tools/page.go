package tools

import (
	"sync"

	"github.com/lucasmirandoliveira/experiments/apps/agent-runtime/internal/agent"
)

// Page is the latest snapshot of one sandbox, plus the mapping a ref needs to
// become a DevTools node handle.
//
// The mapping stays here rather than in agent.Snapshot on purpose: a reasoner gets
// refs and labels, and never a node id it could pass straight to the browser.
type Page struct {
	mu       sync.Mutex
	snapshot agent.Snapshot
	have     bool
	version  int
	backends map[string]int
}

// NewPage returns an empty page store.
func NewPage() *Page {
	return &Page{backends: map[string]int{}}
}

// Stamp files a fresh inspection under the next version and returns it with the
// version filled in. Every earlier ref is now stale.
func (p *Page) Stamp(s agent.Snapshot) agent.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.version++
	s.Version = p.version
	for i := range s.Nodes {
		s.Nodes[i].Ref.Version = s.Version
	}
	p.snapshot = s
	p.have = true
	p.backends = map[string]int{}
	return s
}

// Get returns the current snapshot.
func (p *Page) Get() (agent.Snapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot, p.have
}

// Invalidate bumps the version after an operation changed the document, which
// makes every ref from the previous snapshot stale.
func (p *Page) Invalidate() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.version++
	p.snapshot.Version = p.version
	return p.version
}

// Version reports the current snapshot version.
func (p *Page) Version() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.version
}

// RememberBackend records the DevTools node id behind a ref.
func (p *Page) RememberBackend(ref agent.Ref, backendNodeID int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backends[ref.String()] = backendNodeID
}

// Backend returns the DevTools node id behind a ref, valid only for the snapshot
// that ref names.
func (p *Page) Backend(ref agent.Ref) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ref.Version != p.version {
		return 0, false
	}
	id, ok := p.backends[ref.String()]
	return id, ok
}
