package persist

// Seeding: writing known state into a session's page before it is held, so a run
// measures blobs of a realistic size instead of the 68 bytes an idle page
// produces. A real product page carries cookies and a few hundred KiB of
// localStorage; without this, every run reports sizes that say nothing about the
// store's behaviour.
//
// The values are deterministic: the same session number produces the same bytes,
// so two runs at one seed size are comparable, and the content is pseudo-random
// rather than repeated, so gzip cannot collapse it to a rounding error.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// Seeder writes state into a session's context before the session is held.
type Seeder interface {
	Seed(ctx context.Context, session *browser.Session, sessionID int64) error
}

// Seed writes `Bytes` of localStorage across `Items` keys, and `Cookies` cookies
// on the page's origin. Zero for both means no seeding.
type Seed struct {
	Bytes   int
	Items   int
	Cookies int
	Log     *log.Logger
}

// Seed runs the script once on the session's page.
func (s Seed) Seed(ctx context.Context, session *browser.Session, sessionID int64) error {
	if s.Total() == 0 {
		return nil
	}
	if session == nil {
		return fmt.Errorf("seed: no session")
	}
	script := seedScript(sessionID, s.Bytes, s.Items, s.Cookies)
	result, err := session.Do(ctx, "Runtime.evaluate", map[string]any{
		"expression": script,
		// The script is synchronous; returnByValue keeps the reply small.
		"returnByValue": true,
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	// A page whose document has not committed yet answers with an exception
	// rather than an error — localStorage on an opaque origin throws. That has
	// to surface here, or a run reports "seeded" and captures 68 bytes.
	var reply struct {
		Result struct {
			Type  string `json:"type"`
			Value int    `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &reply); err != nil {
		return fmt.Errorf("seed: decode reply: %w", err)
	}
	if reply.ExceptionDetails != nil {
		detail := reply.ExceptionDetails.Text
		if reply.ExceptionDetails.Exception != nil && reply.ExceptionDetails.Exception.Description != "" {
			detail = reply.ExceptionDetails.Exception.Description
		}
		return fmt.Errorf("seed: %s", detail)
	}
	if s.Log != nil {
		s.Log.Printf("persist: seeded session %d with %d bytes over %d keys and %d cookies (localStorage now holds %d keys)",
			sessionID, s.Bytes, s.Keys(), s.Cookies, reply.Result.Value)
	}
	return nil
}

// Total reports whether this Seed writes anything.
func (s Seed) Total() int { return s.Bytes + s.Cookies }

// Keys is the number of localStorage keys the seed writes.
func (s Seed) Keys() int {
	if s.Items < 1 {
		return 1
	}
	return s.Items
}

// seedScript is the JavaScript that writes the state. A xorshift generator keyed
// by the session number fills each key, so the value is reproducible and
// incompressible.
func seedScript(sessionID int64, bytes, items, cookies int) string {
	keys := items
	if keys < 1 {
		keys = 1
	}
	per := 0
	if bytes > 0 {
		per = bytes / keys
	}
	return fmt.Sprintf(`(function(){
  var seed=%d>>>0, letters='abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
  function next(){seed^=seed<<13;seed>>>=0;seed^=seed>>>17;seed^=seed<<5;seed>>>=0;return seed;}
  var per=%d, keys=%d;
  for(var i=0;i<keys;i++){
    var s='';
    for(var n=0;n<per;n++){s+=letters.charAt(next()%%letters.length);}
    localStorage.setItem('seed-'+i, s);
  }
  var cookies=%d;
  for(var c=0;c<cookies;c++){
    document.cookie='seed-'+c+'='+next().toString(36)+'; path=/';
  }
  return localStorage.length;
})()`, sessionID, per, keys, cookies)
}
