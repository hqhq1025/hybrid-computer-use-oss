package main

import "testing"

func TestProviderActionObservationCanSkipSnapshot(t *testing.T) {
	request := axServiceRequest{
		Action:  axKeypressCommand{Key: "ENTER"},
		Observe: observeNone,
	}
	options := providerObservationOptionsFromOperation(request)
	if options.CaptureSnapshot == nil || *options.CaptureSnapshot {
		t.Fatalf("observe=none action must disable provider snapshot capture: %#v",
			options)
	}

	request.Observe = observeDiff
	options = providerObservationOptionsFromOperation(request)
	if options.CaptureSnapshot == nil || !*options.CaptureSnapshot {
		t.Fatalf("observe=diff action must request provider snapshot capture: %#v",
			options)
	}

	capture := true
	request.Observe = observeNone
	request.CaptureSnapshot = &capture
	options = providerObservationOptionsFromOperation(request)
	if options.CaptureSnapshot == nil || !*options.CaptureSnapshot {
		t.Fatalf("internal continuity capture override was ignored: %#v", options)
	}

	windowAction := WindowOperation("activate")
	options = providerObservationOptionsFromOperation(axServiceRequest{
		WindowAction: &windowAction,
		Observe:      observeNone,
	})
	if options.CaptureSnapshot == nil || !*options.CaptureSnapshot {
		t.Fatalf("window actions must retain internal lifecycle observation: %#v",
			options)
	}

	options = providerObservationOptionsFromOperation(axServiceRequest{
		Operation: axServiceObserve,
	})
	if options.CaptureSnapshot != nil {
		t.Fatalf("read operations should use provider default capture behavior: %#v",
			options)
	}
}
