package persist

// Seeding: writing a dataset into a session's page before it is held, so a run
// measures blobs of the sizes a real product page carries. Without it every blob
// is the 68 bytes an idle page produces, and the store's behaviour at 250 KiB is
// a guess.
//
// The values are deterministic: the same session number produces the same bytes,
// and the content is pseudo-random rather than repeated, so gzip cannot collapse
// a megabyte to a rounding error.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/lucasmirandoliveira/experiments/apps/session-lab/internal/runtime/browser"
)

// Seeder writes one dataset's state into a session's page before the hold.
type Seeder interface {
	Seed(ctx context.Context, session *browser.Session, sessionID int64) error
	// Dataset names the class this session carries, so captures can be counted
	// per class rather than in one aggregate.
	Dataset(sessionID int64) string
}

// Seed writes the state its plan assigns to each session.
type Seed struct {
	Plan Plan
	Log  *log.Logger
}

// Dataset is the class this session carries.
func (s Seed) Dataset(sessionID int64) string { return s.Plan.For(sessionID).Label }

// Seed runs the dataset's script on the session's page.
func (s Seed) Seed(ctx context.Context, session *browser.Session, sessionID int64) error {
	dataset := s.Plan.For(sessionID)
	if dataset.Kind == KindNone {
		return nil
	}
	if session == nil {
		return fmt.Errorf("seed: no session")
	}
	script := seedScript(dataset, sessionID)
	result, err := session.Do(ctx, "Runtime.evaluate", map[string]any{
		"expression":    script,
		"returnByValue": true,
		// Cache Storage and IndexedDB are promises; without this the script
		// returns before it has written anything.
		"awaitPromise": true,
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	// A throw is reported in exceptionDetails, not as an error. That is how the
	// first version of this wrote into about:blank and reported success.
	var reply struct {
		Result struct {
			Value json.RawMessage `json:"value"`
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
		var summary string
		if len(reply.Result.Value) > 0 {
			summary = string(reply.Result.Value)
		}
		s.Log.Printf("persist: seeded session %d with %s %s", sessionID, dataset.Label, summary)
	}
	return nil
}

// seedScript builds the JavaScript for one dataset. Every kind shares a
// deterministic generator keyed by the session id, so two runs at one mix write
// the same bytes into the same sessions.
func seedScript(dataset Dataset, sessionID int64) string {
	count := dataset.Count
	if count < 1 {
		count = 1
	}
	per := 0
	if dataset.Bytes > 0 {
		per = dataset.Bytes / count
	}
	header := fmt.Sprintf(`(async function(){
  var seed=%d>>>0, letters='abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
  function next(){seed^=seed<<13;seed>>>=0;seed^=seed>>>17;seed^=seed<<5;seed>>>=0;return seed;}
  function fill(n){var s='';for(var i=0;i<n;i++){s+=letters.charAt(next()%%letters.length);}return s;}
  var bytes=0, count=%d;`, sessionID, count)

	switch dataset.Kind {
	case KindLocal:
		return header + fmt.Sprintf(`
  for(var i=0;i<count;i++){var value=fill(%d);localStorage.setItem('seed-'+i,value);bytes+=value.length;}
  return JSON.stringify({dataset:%q,keys:localStorage.length,bytes:bytes});})()`, per, dataset.Label)

	case KindSession:
		return header + fmt.Sprintf(`
  for(var i=0;i<count;i++){var value=fill(%d);sessionStorage.setItem('seed-'+i,value);bytes+=value.length;}
  return JSON.stringify({dataset:%q,keys:sessionStorage.length,bytes:bytes});})()`, per, dataset.Label)

	case KindCookies:
		return header + fmt.Sprintf(`
  for(var i=0;i<count;i++){var value=fill(16);document.cookie='seed-'+i+'='+value+'; path=/';bytes+=value.length;}
  return JSON.stringify({dataset:%q,cookies:document.cookie.split(';').length,bytes:bytes});})()`, dataset.Label)

	case KindIndexedDB:
		return header + fmt.Sprintf(`
  var db=await new Promise(function(resolve,reject){
    var request=indexedDB.open('seed-db',1);
    request.onupgradeneeded=function(){request.result.createObjectStore('items',{keyPath:'id'});};
    request.onsuccess=function(){resolve(request.result);};
    request.onerror=function(){reject(request.error);};
  });
  var tx=db.transaction('items','readwrite'), store=tx.objectStore('items');
  for(var i=0;i<count;i++){var body=fill(%d);store.put({id:i,blob:body});bytes+=body.length;}
  await new Promise(function(resolve){tx.oncomplete=resolve;tx.onerror=resolve;tx.onabort=resolve;});
  db.close();
  return JSON.stringify({dataset:%q,records:count,bytes:bytes});})()`, per, dataset.Label)

	case KindCache:
		return header + fmt.Sprintf(`
  var cache=await caches.open('seed-cache');
  for(var i=0;i<count;i++){var body=fill(%d);await cache.put(new Request('/seed-'+i), new Response(body,{headers:{'Content-Type':'text/plain'}}));bytes+=body.length;}
  return JSON.stringify({dataset:%q,entries:(await cache.keys()).length,bytes:bytes});})()`, per, dataset.Label)
	}
	return header + `
  return JSON.stringify({dataset:'none',bytes:0});})()`
}
