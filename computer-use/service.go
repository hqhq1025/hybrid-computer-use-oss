package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type failedObservationEntry struct {
	continuityNote string
	query          string
	windowHandle   string
	aliases        map[string]bool
}

type observationLedger struct {
	lastComplete map[string]*appSnapshot
	lastFullTree map[string]bool
}

type service struct {
	backend                   PlatformBackend
	snapshots                 map[string]*appSnapshot
	guiSnapshot               *appSnapshot
	lastDiagnostics           map[string]map[string]string
	observations              observationLedger
	exposedHiddenRefs         map[string]map[int]elementRecord
	refRegistry               *RefRegistry
	windowRefRegistry         *WindowRefRegistry
	appRefRegistry            *AppRefRegistry
	findCursorRegistry        *FindCursorRegistry
	pendingFailedObservations []failedObservationEntry
	protocolVersion           string
}

func newService() *service {
	return newServiceWithBackend(defaultRuntimeBackend())
}

func newServiceWithBackend(backend PlatformBackend) *service {
	if backend == nil {
		backend = defaultRuntimeBackend()
	}
	return &service{
		backend:         backend,
		snapshots:       map[string]*appSnapshot{},
		lastDiagnostics: map[string]map[string]string{},
		observations: observationLedger{
			lastComplete: map[string]*appSnapshot{},
			lastFullTree: map[string]bool{},
		},
		exposedHiddenRefs:         map[string]map[int]elementRecord{},
		refRegistry:               NewRefRegistry(defaultAXRefRegistryLimit),
		windowRefRegistry:         NewWindowRefRegistry(defaultAXWindowRefRegistryLimit),
		appRefRegistry:            NewAppRefRegistry(defaultAXAppRefRegistryLimit),
		findCursorRegistry:        NewFindCursorRegistry(defaultAXFindCursorLimit),
		pendingFailedObservations: []failedObservationEntry{},
		protocolVersion:           mcpProtocolLegacy,
	}
}

// errorContextEnabled reports whether failures write an error-context file.
// It defaults to on, unlike file output mode, for the opposite reason: file
// output changes the shape of every successful call, while error context is
// written only on failure, when nothing else allows a post-mortem because the
// response holds one error line and the UI has already moved on.
func errorContextEnabled() bool {
	return !strings.EqualFold(
		strings.TrimSpace(os.Getenv("COMPUTER_USE_ERROR_CONTEXT")), "off")
}

// writeErrorContext writes the UI state at the time of a failure to disk. It
// returns a note describing the files and whether fresh UI was sampled.
//
// A one-line error cannot be diagnosed afterwards, so each failure saves a
// snapshot. On the
// desktop the UI changes a moment later, and no tree from the failure would
// survive otherwise.
//
// The state is re-captured rather than taken from the cache: failures often
// happen because the UI no longer matches the snapshot, so the cached copy
// would discard the very evidence being sought. The cost is paid only on the
// failure path.
//
// Capturing can itself fail (the app exited, or the accessibility bus
// disconnected). It then attempts no recovery and returns an empty note: a
// diagnostic facility must not add a new error when diagnosis fails.
func (s *service) writeErrorContext(app, tool, message string) (string, bool) {
	if !errorContextEnabled() || strings.TrimSpace(app) == "" {
		return "", false
	}
	noRefs := false
	response, err := s.callProviderObserve(ProviderObserveRequest{
		ProtocolVersion: providerProtocolV2,
		App:             app,
		Observation: ProviderObservationOptions{
			Boxes: &noRefs,
		},
	})
	if err != nil || response == nil || !response.OK || response.Snapshot == nil {
		return "", false
	}
	dir := filepath.Join(outputDir(), "error-context")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", true
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", true
	}
	stamp := time.Now().UnixNano()
	snapshot := response.Snapshot
	var body strings.Builder
	fmt.Fprintf(&body, "# error context\n\ntool: %s\napp: %s\ncaptured: %s\n\n",
		tool, app, time.Now().Format(time.RFC3339Nano))
	fmt.Fprintf(&body, "## error\n\n%s\n\n", message)
	fmt.Fprintf(&body, "## window\n\n%s\n\n", snapshot.WindowTitle)
	fmt.Fprintf(&body, "## tree at failure time\n\n%s\n", snapshot.renderedText())
	textPath := filepath.Join(dir, fmt.Sprintf("error-context-%d.md", stamp))
	if err := os.WriteFile(textPath, []byte(body.String()), 0o600); err != nil {
		return "", true
	}
	parts := []string{textPath}
	if snapshot.ScreenshotPNGBase64 != "" {
		if raw, decodeErr := base64.StdEncoding.DecodeString(snapshot.ScreenshotPNGBase64); decodeErr == nil {
			extension := ".png"
			if snapshot.screenshotMimeType() == "image/jpeg" {
				extension = ".jpg"
			}
			shotPath := filepath.Join(dir, fmt.Sprintf("error-context-%d%s", stamp, extension))
			if os.WriteFile(shotPath, raw, 0o600) == nil {
				parts = append(parts, shotPath)
			}
		}
	}
	return fmt.Sprintf(
		"\n\n[error context written to %s \u2014 the tree and screenshot as they were "+
			"AFTER this failure, re-captured rather than taken from the cached "+
			"snapshot, because the failure usually means the UI is no longer what "+
			"the snapshot said. Set COMPUTER_USE_ERROR_CONTEXT=off to disable.]",
		strings.Join(parts, " and ")), true
}

func withErrorContextEvidence(
	result toolCallResult,
	contextWritten bool,
) toolCallResult {
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	result.StructuredContent["error_context_captured"] = contextWritten
	result.StructuredContent["error_context_fresh_ui_sampled"] = true
	result.StructuredContent["cause_unattributed"] = true
	result.StructuredContent["error_context_fresh_state_cause"] = "unattributed"
	if contextWritten {
		result.StructuredContent["evidence_scope"] =
			"error_with_external_fresh_ui_context"
	} else {
		result.StructuredContent["evidence_scope"] =
			"error_with_unpersisted_fresh_ui_sample"
	}
	return result
}

func (s *service) callTool(
	name string,
	args map[string]any,
) (result toolCallResult) {
	defer func() {
		result = enforceAXImageOutputPolicy(name, args, result)
	}()

	internalName := internalToolName(name)
	result = s.dispatch(internalName, args)
	if internalName != "gui" {
		result = result.withDefaultEvidenceScope(
			toolEvidenceScope(internalName, args),
		)
	}
	// Capture error context in one place: errors are returned from many call
	// sites, and adding the capture to each would miss some.
	if result.IsError && internalName != "gui" {
		// Preparation can remain queued after the failure. Error reporting must
		// not issue a hidden read or replace the explicit stop with recovery.
		if result.StructuredContent["focus_preparation"] != nil ||
			result.StructuredContent["pointer_preparation"] != nil {
			result.StructuredContent["evidence_scope"] = "error_only"
			result.StructuredContent["preflight_only"] = false
			result.StructuredContent["error_context_captured"] = false
			result.StructuredContent["error_context_fresh_ui_sampled"] = false
			return result
		}
		if result.StructuredContent["find_delivery_budget_exhausted"] == true {
			// A sampled/cached read that cannot fit is not a preflight rejection
			// and must not trigger a second, hidden UI read.
			result.StructuredContent["evidence_scope"] = result.StructuredContent["find_read_scope"]
			result.StructuredContent["preflight_only"] = false
			result.StructuredContent["input_events_emitted"] = false
			result.StructuredContent["error_context_captured"] = false
			result.StructuredContent["error_context_fresh_ui_sampled"] = false
			return result
		}
		app := requiredString(args, "app")
		recoveryObservationAttempted := result.StructuredContent != nil &&
			result.StructuredContent["recovery_observation_attempted"] == true
		if recoveryObservationAttempted {
			return result
		}
		// A no-input stale-ref failure intentionally returns no fresh UI state.
		// Capturing error context here would perform the hidden read that the
		// error contract explicitly forbids and reintroduce causal ambiguity.
		observationRequired := result.StructuredContent != nil &&
			result.StructuredContent["observation_required"] == true
		if observationRequired || s.hasPendingFailedObservation(app) {
			if result.StructuredContent == nil {
				result.StructuredContent = map[string]any{}
			}
			result.StructuredContent["observation_required"] = true
			result.StructuredContent["evidence_scope"] = "error_only"
			if !observationRequired && len(result.Content) > 0 {
				result.Content[0].Text = strings.TrimSpace(result.Content[0].Text) +
					"\n\nAn explicit AX mode=observe or mode=find call is still " +
					"required after the earlier stale-reference failure. That " +
					"failed action emitted no input and did not cause a UI change. " +
					"This error did not perform a recovery read."
			}
			return result
		}
		if result.StructuredContent != nil &&
			result.StructuredContent["assertion_sampled_live_ui"] == true {
			result.StructuredContent["evidence_scope"] =
				"independent_ui_read"
			result.StructuredContent["error_context_captured"] = false
			result.StructuredContent["error_context_fresh_ui_sampled"] = true
			result.StructuredContent["preflight_only"] = false
			result.StructuredContent["cause_unattributed"] = true
			result.StructuredContent["error_context_fresh_state_cause"] =
				"assertion_poll"
			return result
		}
		if result.StructuredContent != nil &&
			result.StructuredContent["poll_attempted"] == true {
			result.StructuredContent["evidence_scope"] = "runtime_poll"
			result.StructuredContent["error_context_captured"] = false
			result.StructuredContent["error_context_fresh_ui_sampled"] = false
			result.StructuredContent["preflight_only"] = false
			return result
		}
		if result.StructuredContent != nil &&
			result.StructuredContent["evidence_scope"] == "error_only" {
			result.StructuredContent["error_context_captured"] = false
			result.StructuredContent["error_context_fresh_ui_sampled"] = false
			if _, known := result.StructuredContent["preflight_only"]; !known {
				result.StructuredContent["preflight_only"] = true
			}
			return result
		}
		message := ""
		for _, item := range result.Content {
			if item.Type == "text" {
				message += item.Text
			}
		}
		// The accessibility bridge itself timed out. Re-observing the app for
		// error context would hit the same bridge and double the delay.
		if strings.Contains(message, "runtime_timeout=true") {
			return result
		}
		note, sampled := s.writeErrorContext(app, internalName, message)
		if note != "" {
			result.Content = append(result.Content, contentItem{Type: "text", Text: note})
		}
		if sampled {
			result = withErrorContextEvidence(result, note != "")
			if note == "" {
				result.Content = append(result.Content, contentItem{
					Type: "text",
					Text: "[error context fresh UI sampled after the failure, " +
						"but the context file could not be written. The sample " +
						"is not included here and its cause is unattributed.]",
				})
			}
		}
	}
	return result
}

func internalToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ax", "a11y":
		return "a11y"
	case "gui":
		return "gui"
	default:
		return name
	}
}

func toolEvidenceScope(name string, args map[string]any) string {
	name = internalToolName(name)
	switch name {
	case "gui":
		return "full_desktop_observation"
	case "a11y":
		mode, _ := parseA11yMode(args)
		switch mode {
		case a11yModeObserve:
			if defaultString(optionalString(args, "observation_target"), "app") == "apps" {
				return "app_inventory"
			}
			return "independent_ui_read"
		case a11yModeFind, a11yModeAssert:
			return "independent_ui_read"
		default:
			return "delivery_or_local_ui"
		}
	default:
		return "delivery_or_local_ui"
	}
}

func (s *service) dispatch(name string, args map[string]any) toolCallResult {
	if !toolIsEnabled(name) {
		// The tool is already absent from tools/list, but a direct call must
		// still be rejected, and the error must say that the channel is
		// disabled rather than that the tool does not exist. An ambiguous
		// error makes the agent suspect a misspelled name.
		return textResult(fmt.Sprintf(
			"tool %q belongs to the %s channel, which is disabled by COMPUTER_USE_CHANNELS. Enabled channels: %s.",
			name, toolChannel[name], strings.Join(sortedChannels(), ", ")), true)
	}
	// The observation budget is parsed the same way for every action tool, so
	// callers need not learn per-tool semantics or defaults. Read-only tools
	// are exempt because they have their own options.
	obs, obsErr := parseObservation(args)
	if obsErr != nil && isActionTool(name) {
		return textResult(obsErr.Error(), true)
	}
	// The verification plan is parsed next to the observation budget for the
	// same reason. The two are orthogonal (obs decides how much to return,
	// plan decides what to verify), so each is parsed separately. An invalid
	// expect or settle_ms is a call error (isError), distinct from an unmet
	// expect, which is a fact about the UI.
	plan, planErr := parseSettlePlan(args)
	if planErr != nil && isActionTool(name) {
		return textResult(planErr.Error(), true)
	}
	switch name {
	case "gui":
		invalidFields := make([]string, 0)
		for field := range args {
			if field != "actions" && field != "settle_ms" {
				invalidFields = append(invalidFields, field)
			}
		}
		sort.Strings(invalidFields)
		if len(invalidFields) > 0 {
			return textResult(
				"GUI accepts only actions and optional settle_ms; unsupported field(s): "+
					strings.Join(invalidFields, ", "),
				true,
			).withEvidenceScope("error_only")
		}
		actions, ok := args["actions"]
		if !ok {
			return textResult(
				`GUI requires actions; use {"actions":[{"type":"screenshot"}]} for an immediate observation`,
				true,
			).withEvidenceScope("error_only")
		}
		if items, ok := actions.([]any); ok && len(items) == 0 {
			return textResult(
				`GUI actions must not be empty; use one screenshot action for observation`,
				true,
			).withEvidenceScope("error_only")
		}
		if items, ok := actions.([]any); ok && len(items) == 1 {
			action, _ := items[0].(map[string]any)
			if strings.EqualFold(optionalString(action, "type"), "screenshot") {
				if _, hasSettle := args["settle_ms"]; hasSettle {
					return textResult(
						"GUI screenshot observation does not accept settle_ms",
						true,
					).withEvidenceScope("error_only")
				}
			}
		}
		if items, ok := actions.([]any); ok && len(items) > 1 {
			for _, item := range items {
				action, _ := item.(map[string]any)
				if strings.EqualFold(optionalString(action, "type"), "screenshot") {
					return textResult(
						"GUI screenshot must be the only action because no intermediate screenshot is returned",
						true,
					).withEvidenceScope("error_only")
				}
			}
		}
		if plan.settleMS > guiMaxSettleMS {
			return textResult(fmt.Sprintf(
				"gui settle_ms must be between 0 and %d", guiMaxSettleMS), true)
		}
		return s.gui(
			actions,
			plan.settleMS,
		)
	case "a11y":
		return s.a11yRequest(args, obs)
	default:
		return textResult(fmt.Sprintf("unsupportedTool(%q)", name), true)
	}
}

func (s *service) listApps() toolCallResult {
	response, err := s.callProviderObserveApps(ProviderObserveAppsRequest{
		ProtocolVersion: providerProtocolV2,
	})
	if err != nil {
		var timeout *runtimeTimeoutError
		if errors.As(err, &timeout) {
			return textResult(
				err.Error()+"\nruntime_timeout=true mutation_unknown=false "+
					"a11y_responsive=false cached_pid_present=unknown "+
					"window_for_cached_pid=unknown. Accessibility application enumeration "+
					"did not answer.", true)
		}
		return textResult(err.Error(), true)
	}
	if !response.OK {
		return textResult(response.Error, true)
	}
	if len(response.Apps) > 0 {
		lines := make([]string, 0, len(response.Apps))
		for _, app := range response.Apps {
			line := strings.TrimSpace(app.DisplayText)
			if line == "" {
				line = strings.TrimSpace(app.Name)
			}
			if app.InputCapable {
				ref := s.appRefRegistry.Acquire(
					app.Name,
					app.ProviderBusName,
					app.ProcessGeneration,
					"",
				)
				if ref != "" {
					if app.Name != "" && strings.HasPrefix(line, app.Name) {
						line = app.Name + " [ref=" + ref + "]" +
							strings.TrimPrefix(line, app.Name)
					} else {
						line = fmt.Sprintf("%s [ref=%s] %s", app.Name, ref, line)
					}
				}
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			response.Text = strings.Join(lines, "\n")
		}
	}
	if strings.TrimSpace(response.Text) == "" {
		response.Text = "No running top-level apps are visible to this accessibility provider."
	}
	return textResult(response.Text, false)
}
