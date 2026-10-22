package main

import (
	"errors"
	"fmt"
	"time"
)

// storedRequest is the response to a call that carried a request_id.
type storedRequest struct {
	key      string
	response any
	done     chan struct{}
	created  time.Time
}

// errRequestIDReused reports a request_id sent again with a different call.
var errRequestIDReused = errors.New("request_id was already used for a different call; use a new request_id for a new call")

// replayOnce runs a call at most once per request_id. A repeated id with the
// same key waits for the first call and returns its stored response, with
// replayed=true and no new input; the same id with another key is an error.
// The store is desktop state, so a client that lost its connection and
// reconnects on a new session still collects its result. It must be called
// without holding the desktop lock: run takes that lock itself, and a
// duplicate waits for the first call outside it. replayOnly refuses an
// absent result instead of treating an evicted request as fresh input.
func (d *desktopShared) replayOnce(id, key string, replayOnly bool, run func() any) (any, bool, error) {
	if len(id) > requestIDMaxLength {
		return nil, false, fmt.Errorf("request_id is limited to %d characters", requestIDMaxLength)
	}
	d.requestMu.Lock()
	d.pruneRequestResults(time.Now())
	if stored, found := d.requestResults[id]; found {
		d.requestMu.Unlock()
		if stored.key != key {
			return nil, false, errRequestIDReused
		}
		<-stored.done
		d.requestMu.Lock()
		completed, available := d.requestResults[id]
		d.requestMu.Unlock()
		if !available || completed.done != stored.done {
			return nil, false, errReplayUnavailable
		}
		return completed.response, true, nil
	}
	if replayOnly {
		d.requestMu.Unlock()
		return nil, false, errReplayUnavailable
	}
	entry := storedRequest{key: key, done: make(chan struct{}), created: time.Now()}
	d.requestResults[id] = entry
	d.requestMu.Unlock()

	response := run()

	d.requestMu.Lock()
	entry.response = response
	d.requestResults[id] = entry
	d.requestMu.Unlock()
	close(entry.done)
	return response, false, nil
}

func (d *desktopShared) pruneRequestResults(now time.Time) {
	for id, stored := range d.requestResults {
		select {
		case <-stored.done:
		default:
			continue
		}
		if now.Sub(stored.created) > requestResultTTL {
			delete(d.requestResults, id)
		}
	}
	for len(d.requestResults) > maxRequestResults {
		oldestID := ""
		var oldest time.Time
		for id, stored := range d.requestResults {
			select {
			case <-stored.done:
			default:
				continue
			}
			if oldestID == "" || stored.created.Before(oldest) {
				oldestID, oldest = id, stored.created
			}
		}
		if oldestID == "" {
			break
		}
		delete(d.requestResults, oldestID)
	}
}
