package main

import (
	"errors"
	"fmt"
	"time"
)

// contractVersion is the version of the code-facing contract: the published
// tool schemas, the records envelope and the error table below. Adding an
// optional field or operation raises the minor number; removing or changing
// one raises the major number.
const contractVersion = "1.0"

// axCodeGUIFrameStale is reported in records when GUI coordinates refer to
// a screenshot that no longer matches the desktop layout. The GUI text
// contract describes the same case in prose.
const axCodeGUIFrameStale = "gui_frame_stale"

// publicError describes one public error code for code callers. It is the
// only table: the schema output, the CLI exit codes and the SDK exception
// classes all come from it.
type publicError struct {
	// ExitCode is the CLI exit status.
	ExitCode int `json:"exit_code"`
	// InputEmitted says whether input may already have been sent when this
	// code is returned: "no", "maybe" (read completed/input_emitted), or
	// "yes".
	InputEmitted string `json:"input_emitted"`
	// Recovery is the next step a caller should take.
	Recovery string `json:"recovery"`
}

var publicErrorCodes = map[string]publicError{
	axCodeInvalidArgument: {2, "no", "fix the argument named by path; hint is a corrected call"},
	axCodeWaitTimeout:     {3, "maybe", "observe again; completed counts the steps that ran and the message says what the wait saw"},
	axCodeStaleRef:        {4, "no", "observe or find again and use the new ref; refs never rebind"},
	axCodeGUIFrameStale:   {4, "no", "take a new GUI screenshot and plan coordinates from it"},
	axCodeWindowChanged:   {5, "maybe", "observe before retrying; do not repeat the batch blindly"},
	axCodeEffectUnknown:   {5, "maybe", "observe before retrying; input may have taken effect"},
	axCodeAppUnavailable:  {6, "no", "list apps; the application is not running or not accessible"},
	axCodeAmbiguousWindow: {6, "no", "use a longer title or the wN from apps"},
	axCodeWindowNotFound:  {6, "no", "list apps and use a wN or an exact title"},
	axCodeNoFocusedWindow: {6, "no", "activate a window first or pass window_ref"},
	axCodeRuntimeError:    {10, "maybe", "retry reads later; observe before retrying input"},
}

// describeContract is the machine-readable contract: what `schema` prints
// and what the SDK and the PTC adapter read at runtime instead of a
// generated copy.
func describeContract() map[string]any {
	tools := []map[string]any{}
	for _, tool := range allToolDefinitions() {
		tools = append(tools, map[string]any{
			"name":         tool.Name,
			"title":        tool.Title,
			"description":  tool.Description,
			"input_schema": tool.InputSchema,
		})
	}
	return map[string]any{
		"contract_version": contractVersion,
		"version":          version,
		"tools":            tools,
		"errors":           publicErrorCodes,
		"usage_exit_code":  64,
		"records": map[string]any{
			"formats":          []string{formatText, formatRecords},
			"records_only_op":  map[string]any{"mode": recordsOpRead, "refs": "1 to 200 element refs rN"},
			"call_options":     []string{"format", "request_id", "since", "refs"},
			"request_id_ttl_s": int(requestResultTTL / time.Second),
		},
	}
}

// storedRequest is the response to a call that carried a request_id.
type storedRequest struct {
	key      string
	response any
	done     chan struct{}
	created  time.Time
}

const (
	requestResultTTL   = 10 * time.Minute
	maxRequestResults  = 512
	requestIDMaxLength = 128
)

// errRequestIDReused reports a request_id sent again with a different call.
var errRequestIDReused = errors.New("request_id was already used for a different call; use a new request_id for a new call")

// replayOnce runs a call at most once per request_id. A repeated id with the
// same key waits for the first call and returns its stored response, with
// replayed=true and no new input; the same id with another key is an error.
// The store is desktop state, so a client that lost its connection and
// reconnects on a new session still collects its result. It must be called
// without holding the desktop lock: run takes that lock itself, and a
// duplicate waits for the first call outside it.
func (d *desktopShared) replayOnce(id, key string, run func() any) (any, bool, error) {
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
		response := d.requestResults[id].response
		d.requestMu.Unlock()
		return response, true, nil
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
		if now.Sub(stored.created) > requestResultTTL {
			delete(d.requestResults, id)
		}
	}
	for len(d.requestResults) > maxRequestResults {
		oldestID := ""
		var oldest time.Time
		for id, stored := range d.requestResults {
			if oldestID == "" || stored.created.Before(oldest) {
				oldestID, oldest = id, stored.created
			}
		}
		delete(d.requestResults, oldestID)
	}
}
