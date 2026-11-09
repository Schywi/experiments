package persist

// Datasets: the classes of restorable state a session can carry, and which class
// each session gets. This is the persistence counterpart of the workload's length
// mix — one run covers the whole range instead of six runs covering one point of
// it.
//
// A label names the surface and its size: local-64k is 64 KiB of localStorage,
// idb-256k is 256 KiB of IndexedDB, cache-1m is 1 MiB of Cache Storage,
// cookies-4 is four cookies, session-4k is 4 KiB of sessionStorage, none is a
// session that carries no seeded state.
//
// The mix assigns a weight per label, so `local-64k:10,local-1m:1` puts ten
// sessions on the small dataset for every one on the large one. Assignment is
// deterministic by session id: two runs at one mix inject the same datasets into
// the same sessions.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Kind is the Chromium surface a dataset writes.
type Kind string

const (
	// KindNone seeds nothing; the session carries whatever the page itself wrote.
	KindNone Kind = "none"
	// KindCookies writes cookies on the page's origin.
	KindCookies Kind = "cookies"
	// KindLocal writes localStorage.
	KindLocal Kind = "local"
	// KindSession writes sessionStorage. It cannot survive a navigation, so a
	// restore always reports it missing: the dataset exists to measure that.
	KindSession Kind = "session"
	// KindIndexedDB writes records through the page's own IndexedDB API.
	KindIndexedDB Kind = "idb"
	// KindCache writes entries into Cache Storage.
	KindCache Kind = "cache"
)

// Dataset is one class of state and the size it writes.
type Dataset struct {
	Label string
	Kind  Kind
	// Bytes is the payload the dataset writes. Unused for cookies.
	Bytes int
	// Count is cookies for KindCookies, keys for KindLocal, records for
	// KindIndexedDB and entries for KindCache.
	Count int
}

// Weight is one entry of a mix: a dataset and how many sessions out of the total
// it applies to.
type Weight struct {
	Dataset Dataset
	Count   int
}

// Plan is a corpus: the datasets and the deterministic assignment of session ids
// to them.
type Plan struct {
	weights []Weight
	total   int
}

// Empty reports whether the plan seeds nothing.
func (p Plan) Empty() bool { return p.total == 0 }

// Labels lists the plan's datasets in the order they were written.
func (p Plan) Labels() []string {
	labels := make([]string, 0, len(p.weights))
	for _, weight := range p.weights {
		labels = append(labels, weight.Dataset.Label)
	}
	return labels
}

// For returns the dataset a session carries.
func (p Plan) For(sessionID int64) Dataset {
	if p.total == 0 {
		return Dataset{Label: "none", Kind: KindNone}
	}
	pick := int(hashID(sessionID) % uint64(p.total))
	for _, weight := range p.weights {
		if pick < weight.Count {
			return weight.Dataset
		}
		pick -= weight.Count
	}
	return p.weights[len(p.weights)-1].Dataset
}

// String describes the plan for a log line.
func (p Plan) String() string {
	parts := make([]string, 0, len(p.weights))
	for _, weight := range p.weights {
		parts = append(parts, fmt.Sprintf("%s×%d", weight.Dataset.Label, weight.Count))
	}
	return strings.Join(parts, " ")
}

// ParsePlan reads "local-64k:10,local-1m:2,idb-256k:3,cookies-4:5".
func ParsePlan(spec string) (Plan, error) {
	var plan Plan
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, countText, ok := strings.Cut(part, ":")
		if !ok {
			return Plan{}, fmt.Errorf("dataset %q is not label:count", part)
		}
		count, err := strconv.Atoi(strings.TrimSpace(countText))
		if err != nil || count < 0 {
			return Plan{}, fmt.Errorf("dataset %q: count is not a non-negative number", part)
		}
		dataset, err := ParseDataset(strings.TrimSpace(label))
		if err != nil {
			return Plan{}, err
		}
		if count == 0 {
			continue
		}
		plan.weights = append(plan.weights, Weight{Dataset: dataset, Count: count})
		plan.total += count
	}
	if plan.total == 0 {
		return Plan{}, fmt.Errorf("mix %q carries no sessions", spec)
	}
	return plan, nil
}

// ParseDataset reads one label: "local-256k", "idb-1m", "cookies-4", "none".
func ParseDataset(label string) (Dataset, error) {
	if label == "" {
		return Dataset{}, fmt.Errorf("empty dataset label")
	}
	kindText, sizeText, _ := strings.Cut(label, "-")
	kind := Kind(kindText)
	dataset := Dataset{Label: label, Kind: kind}
	switch kind {
	case KindNone:
		if sizeText != "" {
			return Dataset{}, fmt.Errorf("dataset %q: none takes no size", label)
		}
		return dataset, nil
	case KindCookies:
		count, err := strconv.Atoi(sizeText)
		if err != nil || count <= 0 {
			return Dataset{}, fmt.Errorf("dataset %q: cookies needs a count, e.g. cookies-4", label)
		}
		dataset.Count = count
		return dataset, nil
	case KindLocal, KindSession:
		size, err := parseSize(sizeText)
		if err != nil {
			return Dataset{}, fmt.Errorf("dataset %q: %w", label, err)
		}
		dataset.Bytes = size
		dataset.Count = 4
		return dataset, nil
	case KindIndexedDB, KindCache:
		size, err := parseSize(sizeText)
		if err != nil {
			return Dataset{}, fmt.Errorf("dataset %q: %w", label, err)
		}
		dataset.Bytes = size
		dataset.Count = 8
		return dataset, nil
	}
	return Dataset{}, fmt.Errorf("dataset %q: unknown surface %q, want none|cookies|local|session|idb|cache", label, kindText)
}

// parseSize reads "64k", "256k", "1m", or a plain byte count.
func parseSize(text string) (int, error) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return 0, fmt.Errorf("no size given")
	}
	multiplier := 1
	switch text[len(text)-1] {
	case 'k':
		multiplier = 1024
		text = text[:len(text)-1]
	case 'm':
		multiplier = 1024 * 1024
		text = text[:len(text)-1]
	}
	value, err := strconv.Atoi(text)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%q is not a positive size", text)
	}
	return value * multiplier, nil
}

// hashID spreads a session id over the whole 64-bit range, so a weighted pick
// does not follow the id order. Ids arrive as 1, 2, 3…: taking them modulo the
// total would hand the first N sessions to the first dataset.
func hashID(id int64) uint64 {
	x := uint64(id) + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// DatasetStats is what one dataset cost in one run.
type DatasetStats struct {
	Label    string `json:"label"`
	Sessions int    `json:"sessions"`
	Bytes    int64  `json:"bytes"`
}

// datasetLedger counts captures per dataset, for the run's report.
type datasetLedger struct {
	mu     sync.Mutex
	byName map[string]*DatasetStats
}

func (l *datasetLedger) record(label string, size int64) {
	if l == nil || label == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byName == nil {
		l.byName = map[string]*DatasetStats{}
	}
	entry, ok := l.byName[label]
	if !ok {
		entry = &DatasetStats{Label: label}
		l.byName[label] = entry
	}
	entry.Sessions++
	entry.Bytes += size
}

// list returns the ledger in label order.
func (l *datasetLedger) list() []DatasetStats {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]DatasetStats, 0, len(l.byName))
	for _, entry := range l.byName {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
