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
	Kubeconfig string
	Context    string
}

// Parse reads args (without the program name).
func Parse(args []string) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("session-lab", flag.ContinueOnError)

	var mix string
	var hold time.Duration
	var deadline time.Duration

	fs.IntVar(&cfg.Sessions, "sessions", 0, "number of sessions to generate (required unless --mix carries counts)")
	fs.Float64Var(&cfg.Rate, "rate", 0, "arrival rate in sessions/s; 0 derives it from --overload-factor")
	fs.BoolVar(&cfg.FixedArrival, "fixed-arrival", false, "space arrivals evenly instead of Poisson")
	fs.StringVar(&mix, "mix", "", "length buckets as label:count pairs, e.g. 30s:25,2m:25,5m:25,10m:25")
	fs.DurationVar(&hold, "hold", 0, "hold duration for a single-bucket run")
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
	if err := cfg.buildMix(mix, hold); err != nil {
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

func (c *Config) validate() error {
	switch c.Policy {
	case model.PolicyReuse, model.PolicyDestroy, model.PolicySuspend:
	default:
		return fmt.Errorf("--policy %q is not reuse|destroy|suspend", c.Policy)
	}
	if c.Sessions <= 0 {
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
