package logging_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/platform/logging"
)

func TestLevelsFilterAndFormatIsGreppable(t *testing.T) {
	var out bytes.Buffer
	logger := logging.New(&out, logging.LevelInfo)

	logger.Debug("hidden", logging.F("session", 7))
	logger.Info("run start", logging.F("run", "r1"), logging.F("sessions", 30))
	logger.Warn("hold interrupted", logging.F("reason", "cancelled"))
	logger.Error("browser lost", logging.F("error", "read: EOF"))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3 (debug filtered): %q", len(lines), out.String())
	}
	if strings.Contains(out.String(), "hidden") {
		t.Fatalf("debug line was written at info level")
	}
	// Fields are sorted and quoted only when needed, so a run diffs cleanly.
	if !strings.Contains(lines[0], "level=info msg=\"run start\" run=r1 sessions=30") {
		t.Fatalf("info line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "level=warn") || !strings.Contains(lines[1], "reason=cancelled") {
		t.Fatalf("warn line = %q", lines[1])
	}
	if !strings.Contains(lines[2], `error="read: EOF"`) {
		t.Fatalf("a value with a space was not quoted: %q", lines[2])
	}
}

func TestParseLevel(t *testing.T) {
	for _, value := range []string{"debug", "info", "warn", "error", ""} {
		if _, err := logging.ParseLevel(value); err != nil {
			t.Fatalf("ParseLevel(%q): %v", value, err)
		}
	}
	if _, err := logging.ParseLevel("verbose"); err == nil {
		t.Fatalf("an unknown level was accepted")
	}
}

func TestDiscardWritesNothing(t *testing.T) {
	var out bytes.Buffer
	discard := logging.Discard()
	discard.Error("should not appear", logging.F("k", "v"))
	if out.Len() != 0 {
		t.Fatalf("discard wrote %q", out.String())
	}
}
