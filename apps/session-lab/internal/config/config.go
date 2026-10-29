// Package config turns operator input into a model.RunConfig.
//
// Rules this package follows, from research/session-lifecycle-elasticity/04-deliverable.md:
// the runner has no ceiling, and no remembered measurement is a default. Every
// value is either a flag or something read from the cluster at start-up, so the
// only defaults here are structural (namespace, pool name, output directory).
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/model"
)

// Config is the parsed command line.
type Config struct {
	model.RunConfig
	Kubeconfig  string
	Context     string
	MetricsAddr string
}

// Parse reads args (without the program name).
func Parse(args []string) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("session-lab", flag.ContinueOnError)

	var mix string
	var hold time.Duration
	var duration time.Duration
	var deadline time.Duration

	fs.IntVar(&cfg.Sessions, "sessions", 0, "number of sessions to generate (required unless --mix carries counts)")
	fs.Float64Var(&cfg.Rate, "rate", 0, "arrival rate in sessions/s; 0 derives it from --overload-factor")
	fs.BoolVar(&cfg.FixedArrival, "fixed-arrival", false, "space arrivals evenly instead of Poisson")
	fs.StringVar(&mix, "mix", "", "length buckets as label:count pairs, e.g. 30s:25,2m:25,5m:25,10m:25")
	fs.DurationVar(&hold, "hold", 0, "hold duration for a single-bucket run")
	fs.DurationVar(&duration, "duration", 0, "arrival window; session counts are derived from --rate and the mix proportions")
	fs.StringVar((*string)(&cfg.Policy), "policy", string(model.PolicyReuse), "instance policy: reuse|destroy|suspend")
	fs.StringVar(&cfg.Namespace, "namespace", "sandbox-demo", "namespace holding the pool and the claims")
	fs.StringVar(&cfg.Pool, "pool", "browser-pool", "SandboxWarmPool name")
	fs.StringVar(&cfg.Template, "template", "", "SandboxTemplate name; empty reads it from the pool")
	fs.IntVar(&cfg.Workers, "workers", 0, "concurrent workers; 0 means one goroutine per live session")
	fs.Float64Var(&cfg.OverloadFactor, "overload-factor", 1, "offered load as a multiple of one instance's throughput")
	fs.DurationVar(&deadline, "session-deadline", 0, "per-session deadline; 0 means none")
	fs.Float64Var(&cfg.SampleSeconds, "sample-seconds", 10, "interval between resource samples")
	fs.StringVar(&cfg.CgroupRoot, "cgroup-root", "", "cgroup v2 root for the sampler; read from the node when empty")
	fs.StringVar(&cfg.OutDir, "out", "", "output directory (required)")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "create and delete a small number of real objects, then stop")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig path; empty uses the in-cluster or default config")
	fs.StringVar(&cfg.Context, "context", "", "kubeconfig context")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", ":9109", "address to serve /metrics on; empty disables it")
	fs.StringVar(&cfg.RunID, "run-id", "", "run identifier; defaults to a timestamp")
	fs.StringVar(&cfg.Template, "pool-template", "", "deprecated alias for --template")

	var replicas int
	fs.IntVar(&replicas, "pool-replicas", -1, "replica count to set on the warm pool before the run; -1 leaves the pool alone")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if replicas >= 0 {
		r := int32(replicas)
		cfg.PoolReplicas = &r
	}
	if deadline > 0 {
		cfg.SessionDeadline = deadline
	}
	if cfg.Template == "" {
		cfg.Template = ""
	}
	if cfg.OutDir == "" {
		return nil, fmt.Errorf("--out is required")
	}
	if cfg.RunID == "" {
		cfg.RunID = time.Now().UTC().Format("20060102T150405Z")
	}
	if duration > 0 {
		if err := cfg.applyDuration(duration, mix, hold); err != nil {
			return nil, err
		}
	} else if err := cfg.buildMix(mix, hold); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) buildMix(mix string, hold time.Duration) error {
	if mix != "" {
		buckets, err := ParseMix(mix)
		if err != nil {
			return err
		}
		c.Mix = buckets
		c.Sessions = 0
		for _, b := range buckets {
			c.Sessions += b.Count
		}
		return nil
	}
	if c.Sessions <= 0 {
		if c.DryRun {
			// A dry run supplies its own two-session mix; nothing to build here.
			return nil
		}
		return fmt.Errorf("give --sessions or --mix")
	}
	if hold <= 0 {
		return fmt.Errorf("--hold is required when --mix is not used")
	}
	c.Mix = []model.Bucket{{Label: hold.String(), Hold: hold, Count: c.Sessions}}
	return nil
}

// ParseMix reads "30s:25,2m:25" into buckets, preserving order.
func ParseMix(spec string) ([]model.Bucket, error) {
	var out []model.Bucket
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, countText, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("bucket %q is not label:count", part)
		}
		hold, err := time.ParseDuration(strings.TrimSpace(label))
		if err != nil {
			return nil, fmt.Errorf("bucket %q: %w", part, err)
		}
		var count int
		if _, err := fmt.Sscanf(strings.TrimSpace(countText), "%d", &count); err != nil {
			return nil, fmt.Errorf("bucket %q: count is not a number", part)
		}
		if hold <= 0 || count < 0 {
			return nil, fmt.Errorf("bucket %q: hold must be positive and count non-negative", part)
		}
		out = append(out, model.Bucket{Label: strings.TrimSpace(label), Hold: hold, Count: count})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("mix parsed to no buckets")
	}
	return out, nil
}

// applyDuration turns an arrival window into session counts. The generator is
// untouched: arrivals stay offsets computed from the rate, so a window of D
// seconds at R sessions/s is round(R*D) sessions whose last offset lands at D.
// A mix keeps its proportions.
func (c *Config) applyDuration(window time.Duration, mix string, hold time.Duration) error {
	if c.Rate <= 0 {
		return fmt.Errorf("--duration needs --rate: the window is the session count divided by the rate")
	}
	total := int(window.Seconds() * c.Rate)
	if total < 1 {
		return fmt.Errorf("--duration %s at %.3f/s is fewer than one session", window, c.Rate)
	}
	switch {
	case mix != "":
		buckets, err := ParseMix(mix)
		if err != nil {
			return err
		}
		c.Mix = buckets
	case hold > 0:
		// One length: the count is whatever the window and rate produce.
		c.Mix = []model.Bucket{{Label: hold.String(), Hold: hold, Count: total}}
		c.Sessions = total
		c.Window = window
		return nil
	default:
		return fmt.Errorf("--duration needs --mix (length proportions) or --hold (one length)")
	}
	var given int
	for _, b := range c.Mix {
		given += b.Count
	}
	if given == 0 {
		return fmt.Errorf("--mix carried no sessions")
	}
	scaled := make([]model.Bucket, 0, len(c.Mix))
	remaining := total
	for i, b := range c.Mix {
		count := int(float64(b.Count) / float64(given) * float64(total))
		if i == len(c.Mix)-1 {
			count = remaining
		}
		if b.Count > 0 && count < 1 {
			count = 1
		}
		if count > remaining {
			count = remaining
		}
		remaining -= count
		scaled = append(scaled, model.Bucket{Label: b.Label, Hold: b.Hold, Count: count})
	}
	c.Mix = scaled
	c.Sessions = total
	c.Window = window
	return nil
}

func (c *Config) validate() error {
	switch c.Policy {
	case model.PolicyReuse, model.PolicyDestroy, model.PolicySuspend:
	default:
		return fmt.Errorf("--policy %q is not reuse|destroy|suspend", c.Policy)
	}
	if c.Sessions <= 0 && !c.DryRun {
		return fmt.Errorf("--sessions must be positive, got %d", c.Sessions)
	}
	if c.OverloadFactor <= 0 {
		return fmt.Errorf("--overload-factor must be positive, got %v", c.OverloadFactor)
	}
	if c.SampleSeconds <= 0 {
		return fmt.Errorf("--sample-seconds must be positive")
	}
	if c.PoolReplicas != nil && *c.PoolReplicas < 0 {
		return fmt.Errorf("--pool-replicas must be zero or more")
	}
	if err := os.MkdirAll(c.OutDir, 0o755); err != nil {
		return fmt.Errorf("--out %s: %w", c.OutDir, err)
	}
	abs, err := filepath.Abs(c.OutDir)
	if err != nil {
		return err
	}
	c.OutDir = abs
	return nil
}

// DBPath is the SQLite file for this run.
func (c *Config) DBPath() string { return filepath.Join(c.OutDir, c.RunID+".db") }

// ReportPath is the JSON report for this run.
func (c *Config) ReportPath() string { return filepath.Join(c.OutDir, c.RunID+".json") }
