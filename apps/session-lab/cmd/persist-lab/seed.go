package main

// Seeding known state into each page, so a run measures blobs of a realistic
// size and of the classes a product page carries. A page nobody writes to
// produces 68 bytes; that number says nothing about a store.

import (
	"fmt"
	"log"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/persist"
)

// seedPlan turns the flags into a corpus. --seed-mix is the real interface; the
// older --seed-bytes and --seed-cookies are sugar for a single dataset.
func seedPlan(mix string, bytes, items, cookies int, log *log.Logger) (persist.Plan, error) {
	if mix != "" {
		return persist.ParsePlan(mix)
	}
	if bytes <= 0 && cookies <= 0 {
		return persist.Plan{}, nil
	}
	if bytes > 0 && cookies > 0 {
		return persist.Plan{}, fmt.Errorf("--seed-bytes and --seed-cookies cannot both be used without --seed-mix; express both as a mix")
	}
	if cookies > 0 {
		return persist.ParsePlan(fmt.Sprintf("cookies-%d:1", cookies))
	}
	if log != nil && items > 0 {
		log.Printf("persist: --seed-items is not used by --seed-bytes; a dataset carries its own key count")
	}
	return persist.ParsePlan(fmt.Sprintf("local-%d:1", bytes))
}

// seeder returns nil when the corpus is empty: a nil Seeder is what keeps the
// runner's behaviour unchanged for a run that asks for no seeding.
func seeder(plan persist.Plan, log *log.Logger) persist.Seeder {
	if plan.Empty() {
		return nil
	}
	return persist.Seed{Plan: plan, Log: log}
}
