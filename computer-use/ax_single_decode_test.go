package main

import (
	"encoding/json"
	"errors"
	"testing"
)

// countAXDecodes counts decodeAXRequest calls while run executes.
func countAXDecodes(t *testing.T, run func()) int {
	t.Helper()
	count := 0
	axDecodeHook = func() { count++ }
	defer func() { axDecodeHook = nil }()
	run()
	return count
}

// axSingleDecodeCalls are valid and invalid AX calls. The valid ones reach
// a stub backend that fails every request.
var axSingleDecodeCalls = []string{
	`{"mode":"apps"}`,
	`{"mode":"observe","window_ref":"w1","observe":"tree"}`,
	`{"mode":"observe","app":"a1","root":"focused"}`,
	`{"mode":"find","window_ref":"w1","query":{"role":"button"}}`,
	`{"mode":"find","cursor":"f9"}`,
	`{"mode":"act","window_ref":"w1","actions":[{"type":"keypress","keys":["CTRL","S"]}]}`,
	`{"mode":"act","actions":[{"type":"click","ref":"r1"}],"until":{"type":"exists","ref":"r1","timeout_ms":1}}`,
	`{"mode":"apps","observe":"none"}`,
	`{"mode":"observe","window_ref":"w2","root":"w2"}`,
	`{"mode":"act","actions":[{"type":"close","window_ref":"w2"}]}`,
	`{"mode":"find","window_ref":"w1","query":{"role":"x"},"limit":100}`,
	`{"mode":"observe","app":"x","window_ref":"w1"}`,
	`{"mode":"act","actions":[]}`,
}

// Each AX call is decoded once, and the result equals the earlier path that
// decoded in dispatch and again in dispatchValidated.
func TestAXCallDecodesArgumentsOnce(t *testing.T) {
	for _, raw := range axSingleDecodeCalls {
		current := singleDecodeService(t)
		var got toolCallResult
		if n := countAXDecodes(t, func() {
			got = current.dispatch(publicToolAX, decodeTestArguments(t, raw))
		}); n != 1 {
			t.Fatalf("%s decoded %d times", raw, n)
		}
		earlier := singleDecodeService(t)
		want := func() toolCallResult {
			args := decodeTestArguments(t, raw)
			if _, argErr := decodeAXRequest(args); argErr != nil {
				requested := 0
				if actions, ok := args["actions"].([]any); ok {
					requested = len(actions)
				}
				return axArgErrorResult(argErr, requested)
			}
			if argErr := validateToolArguments(publicToolAX, args); argErr != nil {
				return argumentErrorResult(publicToolAX, args, argErr)
			}
			return earlier.dispatchValidated(publicToolAX, args)
		}()
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("%s changed:\n got %s\nwant %s", raw, gotJSON, wantJSON)
		}
	}
}

func singleDecodeService(t *testing.T) *service {
	service, _, _, _ := sheetBatchService(t)
	service.backend = &recordingPlatformBackend{err: errors.New("no desktop in this test")}
	return service
}
