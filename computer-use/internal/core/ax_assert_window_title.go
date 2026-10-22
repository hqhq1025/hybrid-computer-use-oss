package core

import (
	"fmt"
	"strings"
	"time"
)

// verifyWindowTitle polls the titles of every top-level window of app:
// documents, dialogs and popups alike. A title predicate asks whether the
// application shows such a window, so it is never narrowed to the window a
// batch acted in; that window is often the one the action closed or the one a
// new dialog covers.
func (s *Service) verifyWindowTitle(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	stableMS int,
) ToolCallResult {
	return s.verifyWindowTitleWithProbe(
		app,
		titleSubstring,
		expected,
		timeoutMS,
		stableMS,
		s.callProviderWindowList,
	)
}

func (s *Service) verifyWindowTitleWithProbe(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	stableMS int,
	probe func(ProviderWindowListRequest) (*ProviderWindowResult, error),
) ToolCallResult {
	if timeoutMS <= 0 {
		timeoutMS = 5000
	}
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	var trueSince time.Time
	attempts := 0
	poll := windowTitlePoll{lastTitles: []string{}}
	for {
		attempts++
		operation := AXServiceRequest{
			Operation: axServiceWindowList,
			App:       app,
		}
		// Closing an application's last window can retire its aN before this
		// poll; the read then names the same exact process.
		if err := s.bindPredicateAppIdentity(&operation); err != nil {
			return axAssertionResult(
				"wait: application target: "+err.Error(),
				true,
				"invalid_target",
				attempts,
				stableMS,
			)
		}
		response, err := probe(
			providerWindowListRequestFromOperation(operation),
		)
		matched := poll.record(app, titleSubstring, response, err)
		predicate := len(matched) > 0
		if !expected {
			predicate = !predicate && poll.lastError == ""
		}
		if predicate {
			now := time.Now()
			if trueSince.IsZero() {
				trueSince = now
			}
			stableFor := int(now.Sub(trueSince) / time.Millisecond)
			if stableMS == 0 || stableFor >= stableMS {
				return poll.passResult(
					app, titleSubstring, expected, matched, attempts, stableMS,
				)
			}
		} else {
			trueSince = time.Time{}
		}
		if !time.Now().Before(deadline) {
			return poll.timeoutResult(
				app, titleSubstring, expected, timeoutMS, attempts, stableMS,
			)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// windowTitlePoll keeps what the window-title polls have seen so far. The
// titles and the live-UI flag survive a failed poll; the error is replaced by
// every poll.
type windowTitlePoll struct {
	lastTitles    []string
	lastError     string
	sampledLiveUI bool
}

// record folds one window-list response into the poll and returns the
// titles that contain titleSubstring.
func (poll *windowTitlePoll) record(
	app,
	titleSubstring string,
	response *ProviderWindowResult,
	err error,
) []string {
	matched := []string{}
	if err != nil {
		poll.lastError = err.Error()
	} else if !response.OK {
		// An application that no longer exists, read from a complete
		// inventory, has no window with any title.
		completeAppAbsence := isShortAXAppRef(app) &&
			response.Failure != nil &&
			response.Failure.FactsComplete &&
			strings.EqualFold(response.Failure.Subject, "app") &&
			(response.Failure.Code == "stale_target" ||
				response.Failure.Code == "stale_app_ref" ||
				response.Failure.Code == "ax_app_unavailable")
		if completeAppAbsence {
			poll.lastError = ""
			poll.sampledLiveUI = true
			poll.lastTitles = nil
		} else {
			poll.lastError = response.Error
		}
	} else {
		poll.lastError = ""
		poll.sampledLiveUI = true
		poll.lastTitles = append([]string(nil), response.WindowTitles...)
		for _, title := range response.WindowTitles {
			if containsFold(title, titleSubstring) {
				matched = append(matched, title)
			}
		}
	}
	return matched
}

func (poll *windowTitlePoll) passResult(
	app,
	titleSubstring string,
	expected bool,
	matched []string,
	attempts,
	stableMS int,
) ToolCallResult {
	status := "present"
	observed := matched
	if !expected {
		status = "absent"
		observed = poll.lastTitles
	}
	result := axAssertionResult(
		fmt.Sprintf(
			"PASS: application %q window title containing %q is %s. "+
				"Current window titles: %q.",
			app,
			titleSubstring,
			status,
			observed,
		),
		false,
		"pass",
		attempts,
		stableMS,
	)
	result.StructuredContent["window_titles"] = poll.lastTitles
	result.StructuredContent["window_title_matches"] = matched
	return result
}

// timeoutResult reports a predicate that never held. Only a timeout after a
// complete live read is a negative answer; any other timeout is incomplete
// observation.
func (poll *windowTitlePoll) timeoutResult(
	app,
	titleSubstring string,
	expected bool,
	timeoutMS,
	attempts,
	stableMS int,
) ToolCallResult {
	expectation := "appear"
	if !expected {
		expectation = "disappear"
	}
	text := fmt.Sprintf(
		"wait false: no window title containing %q satisfied the "+
			"requested %s predicate within %dms.",
		titleSubstring,
		expectation,
		timeoutMS,
	)
	if poll.lastError != "" {
		text += "\nLast AX application lookup: " + firstLine(poll.lastError)
	}
	if len(poll.lastTitles) > 0 {
		limit := min(5, len(poll.lastTitles))
		text += fmt.Sprintf(
			"\nCurrent window candidates for %q: %q.",
			app,
			poll.lastTitles[:limit],
		)
	}
	result := ToolCallResult{}
	if poll.sampledLiveUI && poll.lastError == "" {
		result = assertionNegativeResult(
			text,
			"window_title_timeout",
			attempts,
			stableMS,
		)
	} else {
		result = axAssertionResult(
			text,
			true,
			"observation_incomplete",
			attempts,
			stableMS,
		)
	}
	result.StructuredContent["assertion_sampled_live_ui"] =
		poll.sampledLiveUI
	result.StructuredContent["inventory_sample_complete"] =
		poll.sampledLiveUI && poll.lastError == ""
	result.StructuredContent["window_titles"] = poll.lastTitles
	return result
}
