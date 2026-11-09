package main

// Seeding known state into each page, so a run measures blobs of a realistic
// size. A page nobody writes to produces 68 bytes; a product page carries
// cookies and hundreds of KiB of localStorage, and those are the sizes the store
// has to be judged on.

import (
	"log"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/persist"
)

// seeder returns nil when nothing was asked for: a nil Seeder is what keeps the
// runner's behaviour unchanged for every run that does not pass a seed size.
func seeder(bytes, items, cookies int, log *log.Logger) persist.Seeder {
	if bytes <= 0 && cookies <= 0 {
		return nil
	}
	return persist.Seed{Bytes: bytes, Items: items, Cookies: cookies, Log: log}
}
