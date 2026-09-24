package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func optionalSettleMS(args map[string]any) (int, error) {
	value, present := args["settle_ms"]
	if !present || value == nil {
		return defaultSettleMS, nil
	}
	invalid := fmt.Errorf("settle_ms must be a whole number of milliseconds between 0 and %d", maxSettleMS)
	var milliseconds int
	switch typed := value.(type) {
	case int:
		milliseconds = typed
	case float64:
		if !isWholeNumber(typed) || typed < 0 || typed > float64(maxSettleMS) {
			return 0, invalid
		}
		milliseconds = int(typed)
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return 0, invalid
		}
		milliseconds = int(integer)
	case string:
		integer, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, invalid
		}
		milliseconds = integer
	default:
		return 0, invalid
	}
	if milliseconds < 0 || milliseconds > maxSettleMS {
		return 0, invalid
	}
	return milliseconds, nil
}

// settlePlan is the verification intent carried by one action. With a nil
// expect the verification path is skipped entirely and the response is
// unchanged.
type settlePlan struct {
	expect   *expectSpec
	settleMS int
}

func parseSettlePlan(args map[string]any) (settlePlan, error) {
	expect, err := parseExpect(args)
	if err != nil {
		return settlePlan{}, err
	}
	settleMS, err := optionalSettleMS(args)
	if err != nil {
		return settlePlan{}, err
	}
	return settlePlan{expect: expect, settleMS: settleMS}, nil
}

type settleOutcome struct {
	snapshot               *appSnapshot
	snapshotNotes          []string
	snapshotFromFollowup   bool
	verificationIncomplete bool
	followupReadFailed     bool
	verified               bool
	evidence               string
	elapsedMS              int
	polls                  int
}

func (s *service) refreshSnapshotForSettle(
	app string,
	request axServiceRequest,
) (*appSnapshot, []string, toolCallResult) {
	internalPrevious := s.currentSnapshot(app)
	if internalPrevious != nil {
		applyProjection(&request, internalPrevious.projection)
	}
	snapshot, notes, result := s.readSnapshot(app, request)
	if result.IsError {
		return nil, nil, result
	}
	snapshot.projection = projectionFromRequest(request)
	visiblePrevious := s.deliveredSnapshotFor(snapshot)
	notes = append(continuityNotes(visiblePrevious, snapshot), notes...)
	// Settle polls are hidden implementation reads. They must update the action
	// cache, but must not consume lastDiagnostics as though the model had seen
	// each intermediate response.
	s.rememberSnapshot(app, snapshot)
	return snapshot, notes, toolCallResult{}
}

func settleReadRequest(app string, includeScreenshot *bool, screenshotScope string, projection snapshotProjection) axServiceRequest {
	request := axServiceRequest{
		Operation:         axServiceObserve,
		App:               app,
		IncludeScreenshot: includeScreenshot,
		ScreenshotScope:   screenshotScope,
	}
	applyProjection(&request, projection)
	return request
}

// settle polls rather than sleeping for a fixed time: it returns as soon as
// the predicate holds and reports the actual elapsed time.
//
// Timing starts when the action returns, not when it was sent. The action's
// own AT-SPI traversal can take tens of seconds in large apps such as GIMP;
// counting it in settled_after_ms would measure observation speed instead of
// how long the UI took to settle, misleading the caller.
//
// The first evaluation reuses the snapshot the action just built, so it costs
// nothing extra; the common case of a dialog that is already open matches at
// 0ms.
//
// includeScreenshot and screenshotScope are carried over from the action
// request so that re-reads match the requested observation. With
// observe="screenshot" or "full" a re-read still includes an image, with
// "none" or "tree" it does not, and GUI actions keep re-reading the full
// desktop instead of silently falling back to a window screenshot after the
// first poll.
//
// before is the pre-action snapshot and stays fixed for the whole poll as the
// baseline for appears and disappears; polling replaces only the after state.
func (s *service) settle(app string, plan settlePlan, before, first *appSnapshot, includeScreenshot *bool, screenshotScope string) settleOutcome {
	start := time.Now()
	snapshot := first
	var snapshotNotes []string
	snapshotFromFollowup := false
	for attempt := 1; ; attempt++ {
		ok, evidence := plan.expect.evaluate(before, snapshot)
		elapsed := int(time.Since(start) / time.Millisecond)
		if ok {
			return settleOutcome{
				snapshot:             snapshot,
				snapshotNotes:        snapshotNotes,
				snapshotFromFollowup: snapshotFromFollowup,
				verified:             true,
				evidence:             evidence,
				elapsedMS:            elapsed,
				polls:                attempt - 1,
			}
		}
		if elapsed >= plan.settleMS {
			return settleOutcome{
				snapshot:             snapshot,
				snapshotNotes:        snapshotNotes,
				snapshotFromFollowup: snapshotFromFollowup,
				evidence:             evidence,
				elapsedMS:            elapsed,
				polls:                attempt - 1,
			}
		}
		pause := settlePollMS
		if remaining := plan.settleMS - elapsed; remaining < pause {
			pause = remaining
		}
		time.Sleep(time.Duration(pause) * time.Millisecond)
		next, nextNotes, result := s.refreshSnapshotForSettle(
			app, settleReadRequest(app, includeScreenshot, screenshotScope, before.projection))
		if result.IsError || next == nil {
			// A failed re-read must neither be swallowed nor mask the action's
			// own result: the verdict states that it rests only on the last
			// successful snapshot.
			basis := "the state captured immediately after the action"
			if snapshotFromFollowup {
				basis = "the last successful settled follow-up snapshot"
			}
			return settleOutcome{
				snapshot:               snapshot,
				snapshotNotes:          snapshotNotes,
				snapshotFromFollowup:   snapshotFromFollowup,
				verificationIncomplete: true,
				followupReadFailed:     true,
				evidence: evidence + " (a follow-up read of the tree failed, so this verdict " +
					"rests only on " + basis + ")",
				elapsedMS: int(time.Since(start) / time.Millisecond),
				polls:     attempt,
			}
		}
		snapshot = next
		snapshotNotes = nextNotes
		snapshotFromFollowup = true
	}
}

// expectNote is the single renderer for expect results. It starts with a
// machine-readable JSON payload (predicate_verified, completeness flags,
// evidence and settled_after_ms), followed by an explanation for the LLM.
//
// predicate_verified answers only whether the declared accessibility predicate
// held. It neither proves the action's effect nor supplies delivery facts that
// the runtime did not report.
func expectNote(plan settlePlan, outcome settleOutcome, request axServiceRequest, actionNotes []string) string {
	payload, err := json.Marshal(map[string]any{
		"predicate_verified":    outcome.verified,
		"verification_complete": !outcome.verificationIncomplete,
		"followup_read_failed":  outcome.followupReadFailed,
		"evidence":              outcome.evidence,
		"settled_after_ms":      outcome.elapsedMS,
	})
	if err != nil {
		payload = []byte(fmt.Sprintf(`{"predicate_verified": %t, "settled_after_ms": %d}`,
			outcome.verified, outcome.elapsedMS))
	}
	cost := ""
	if outcome.polls > 0 {
		cost = fmt.Sprintf(" It cost %d extra tree read(s) to settle.", outcome.polls)
	}
	if outcome.verificationIncomplete {
		return "[expect] " + string(payload) + " — verification is incomplete because " +
			"a follow-up accessibility read failed. predicate_verified=false is not a " +
			"full-budget timeout verdict and does not show that the predicate stayed " +
			"false for the requested settle interval. The action result and last " +
			"successful snapshot remain available, but observe fresh state before " +
			"retrying or drawing a task-level conclusion." + cost
	}
	if outcome.verified {
		return "[expect] " + string(payload) + " — only the declared accessibility predicate " +
			"held in the observed tree. This does not confirm the action's intended effect, " +
			"delivery, or any state outside that predicate." + cost
	}
	emission := "The action was requested/emitted; delivery to the intended target is unknown."
	if windowFocusWasConfirmed(request) {
		emission = "The input was emitted after the target application window was confirmed focused; delivery to the intended control remains unknown."
	}
	for _, note := range actionNotes {
		if strings.Contains(note, "Delivery to the intended target was not verified") ||
			strings.Contains(note, "not evidence the action") {
			emission = "The action was requested/emitted; delivery to the intended target is unknown."
			break
		}
	}
	return "[expect] " + string(payload) + " — this is NOT a call error and isError is false: " +
		emission + " The declared accessibility predicate did not become true " +
		fmt.Sprintf("within the %dms settle budget. Expected %s. ", plan.settleMS, plan.expect.describe()) +
		"Read the evidence above before retrying: it says what the tree actually shows, which " +
		"usually tells you whether the action missed, whether the effect is one the tree cannot " +
		"represent (formatting, file state, canvas pixels — check the screenshot for those), or " +
		"whether the expectation itself was the wrong thing to wait for. Raising settle_ms only " +
		"helps for the first kind." + cost
}
