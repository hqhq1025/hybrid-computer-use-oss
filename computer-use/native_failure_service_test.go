package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeIdentityConflictPreservesMutationAndRequiresObservation(t *testing.T) {
	for _, emitted := range []bool{false, true} {
		s := newServiceWithBackend(&recordingPlatformBackend{})
		snapshot := nativeAliasFixture("process_instance")
		snapshot.Elements[2].AutomationID = "conflicting-id"
		prepared, result := s.prepareProviderSnapshot(axServiceRequest{App: "a1"},
			&ProviderAccessibilityResult{
				OK: true, Snapshot: snapshot,
				Mutation: &ProviderMutationFacts{InputEventsEmitted: emitted},
			})
		if prepared != nil || !result.IsError ||
			result.StructuredContent["input_events_emitted"] != emitted ||
			result.StructuredContent["mutation_unknown"] != emitted ||
			result.StructuredContent["observation_required"] != true ||
			result.StructuredContent["final_observation_returned"] != false {
			t.Fatalf("conflicting identity exposed trusted state or lost input evidence: %#v", result)
		}
		if !s.hasPendingFailedObservation("a1") {
			t.Fatal("identity conflict did not require independent observation")
		}
		notes := s.consumeFailedObservation("a1", snapshot)
		if !strings.Contains(notes, "does not establish") {
			t.Fatalf("continuity note attributed earlier input: %v", notes)
		}
	}
}

func TestNativeResolveTimeoutDoesNotInventMutation(t *testing.T) {
	for _, action := range []axActionCommand{axResolveTargetCommand{}, axClickCommand{}} {
		for _, failure := range []error{
			&runtimeTimeoutError{duration: time.Second, platform: "macOS"},
			&runtimeTransportError{operation: ProviderWireResolveAndAct, requestSent: true, cause: errors.New("EOF")},
		} {
			s := newServiceWithBackend(&recordingPlatformBackend{})
			request := axServiceRequest{Action: action}
			intent := action.Intent()
			result := s.runtimeFailureResult(request, nil, failure)
			if !result.IsError || result.StructuredContent["mutation_unknown"] != (intent == AccessibilityClick) {
				t.Fatalf("intent=%s failure=%v has incorrect emission uncertainty: %#v", intent, failure, result)
			}
		}
	}
}
