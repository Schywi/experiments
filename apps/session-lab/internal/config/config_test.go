package config

import (
	"strings"
	"testing"
	"time"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/schedule"
)

func TestDurationDerivesAWindowNotABatch(t *testing.T) {
	// A ten-minute window at 0.1 sessions/s is sixty sessions whose arrivals
	// spread across the window. The generator is unchanged: it still places one
	// arrival per offset, the offsets simply cover 600s instead of 75s.
	cfg, err := Parse([]string{
		"--duration", "600s", "--rate", "0.1",
		"--mix", "30s:40,2m:15,5m:5", "--out", t.TempDir(),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Sessions != 60 {
		t.Fatalf("sessions = %d, want 60", cfg.Sessions)
	}
	if cfg.Window != 600*time.Second {
		t.Fatalf("window = %s, want 600s", cfg.Window)
	}
	total := 0
	for _, bucket := range cfg.Mix {
		total += bucket.Count
	}
	if total != 60 {
		t.Fatalf("mix sums to %d, want 60", total)
	}

	plan, err := schedule.Generate(cfg.RunConfig, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(plan.Sessions) != 60 {
		t.Fatalf("planned %d sessions, want 60", len(plan.Sessions))
	}
	// Poisson arrivals: the last offset should land near the window, not at a
	// fraction of it.
	span := plan.Span.Seconds()
	if span < 480 || span > 780 {
		t.Fatalf("arrival span %.0fs is not near the 600s window", span)
	}
	// Arrivals are spread, not bunched: a quarter of the window holds a quarter
	// of the arrivals, give or take.
	quarter := plan.Span / 4
	inFirstQuarter := 0
	for _, session := range plan.Sessions {
		if session.Offset <= quarter {
			inFirstQuarter++
		}
	}
	if inFirstQuarter < 5 || inFirstQuarter > 25 {
		t.Fatalf("%d of 60 arrivals in the first quarter of the window", inFirstQuarter)
	}
}

func TestDurationNeedsARate(t *testing.T) {
	if _, err := Parse([]string{"--duration", "600s", "--mix", "30s:1", "--out", t.TempDir()}); err == nil {
		t.Fatalf("a duration without a rate produced a run")
	}
}

func TestFixedArrivalWindowMatchesTheRequest(t *testing.T) {
	cfg, err := Parse([]string{
		"--duration", "120s", "--rate", "0.5", "--fixed-arrival",
		"--mix", "30s:1", "--out", t.TempDir(),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := schedule.Generate(cfg.RunConfig, 1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// 60 sessions at 0.5/s: the last arrival lands at (n-1)/rate = 118s.
	if got := plan.Span.Seconds(); got < 110 || got > 125 {
		t.Fatalf("fixed arrival span = %.1fs, want ~118s", got)
	}
}

func TestDurationWithOneHoldIsEnough(t *testing.T) {
	// The documented short form: a window, a rate and one length.
	cfg, err := Parse([]string{
		"--duration", "600s", "--rate", "0.1", "--hold", "30s", "--out", t.TempDir(),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Sessions != 60 || len(cfg.Mix) != 1 || cfg.Mix[0].Count != 60 {
		t.Fatalf("sessions=%d mix=%+v, want 60 in one bucket", cfg.Sessions, cfg.Mix)
	}
}

func TestDurationWithoutLengthIsRefusedWithAReason(t *testing.T) {
	_, err := Parse([]string{"--duration", "600s", "--rate", "0.1", "--out", t.TempDir()})
	if err == nil {
		t.Fatalf("a duration with no mix and no hold produced a run")
	}
	if !strings.Contains(err.Error(), "--mix") || !strings.Contains(err.Error(), "--hold") {
		t.Fatalf("unhelpful error: %v", err)
	}
}
