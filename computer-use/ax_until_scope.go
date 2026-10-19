package main

// A selector or title predicate observes the whole application that owns the
// batch or read window: every visible top-level window, including a dialog or
// popup menu that the action opened as a window of its own. A predicate with
// ref rN keeps reading that one element. Reads never re-bind an input target:
// they only sample state, and the batch window stays the target of input.

// untilApplicationRead is one poll of a selector predicate.
type untilApplicationRead struct {
	// primary is the bound window, or the application's main window once the
	// bound window is gone. It is an empty snapshot when the application has
	// exited.
	primary *appSnapshot
	// lookup is primary with its scope applied.
	lookup *appSnapshot
	// windowRef names the one other window that is modal or has focus, or the
	// window that replaced a closed bound window. It goes into the hint of an
	// until that did not hold.
	windowRef string
	request   axServiceRequest
}

// readUntilApplication reads the bound window. The read also returns the
// application's window inventory, which readOtherWindows uses when the
// bound window does not answer the predicate.
func (s *service) readUntilApplication(
	request axServiceRequest,
) (*untilApplicationRead, toolCallResult) {
	app := request.App
	_ = s.bindPredicateAppIdentity(&request)
	boundHandle := request.TargetWindowHandle
	primary, _, result := s.readProjectedSnapshot(app, request)
	if result.IsError && boundHandle != "" && predicateWindowGone(result) {
		// The bound window closed. The application's other windows still
		// answer the predicate. If they cannot be read, the closed-window
		// result stands.
		fallback := request
		fallback.TargetWindowHandle = ""
		if snapshot, _, fallbackResult := s.readProjectedSnapshot(app, fallback); !fallbackResult.IsError {
			primary, result, request = snapshot, fallbackResult, fallback
		} else if predicateAppGone(fallbackResult) {
			result = fallbackResult
		}
	}
	if result.IsError {
		if predicateAppGone(result) {
			// A complete inventory no longer has the process: it has no
			// window and no element.
			empty := &appSnapshot{}
			return &untilApplicationRead{
				primary: empty, lookup: empty, request: request,
			}, toolCallResult{}
		}
		return nil, result
	}
	read := &untilApplicationRead{
		primary: primary,
		lookup:  snapshotForScopedLookup(primary),
		request: request,
	}
	if boundHandle != "" && primary.WindowHandle != boundHandle {
		read.windowRef = primary.publicWindowRef
	}
	if ref := soleFocusedOtherWindowRef(primary); ref != "" {
		read.windowRef = ref
	}
	return read, toolCallResult{}
}

// otherWindows lists the application's visible top-level windows other than
// the one already read.
func (read *untilApplicationRead) otherWindows() []topLevelWindow {
	return otherVisibleWindows(read.primary)
}

func otherVisibleWindows(snapshot *appSnapshot) []topLevelWindow {
	windows := []topLevelWindow{}
	if snapshot == nil {
		return windows
	}
	for _, window := range snapshot.Windows {
		if window.Selected || window.Handle == snapshot.WindowHandle ||
			!(window.Showing || window.Modal || window.Active) {
			continue
		}
		windows = append(windows, window)
	}
	return windows
}

// soleFocusedOtherWindowRef returns the wN of the one other window of the
// snapshot's application that is modal or has focus, such as a dialog the
// action opened, or "" when there is none or more than one.
func soleFocusedOtherWindowRef(snapshot *appSnapshot) string {
	focused := []string{}
	for _, window := range otherVisibleWindows(snapshot) {
		if (window.Modal || window.Active) && window.publicRef != "" {
			focused = append(focused, window.publicRef)
		}
	}
	if len(focused) != 1 {
		return ""
	}
	return focused[0]
}

// readOtherWindows reads every other visible window of the application, one
// provider observe per window, and returns their records as one lookup
// snapshot. complete is false when a window could not be read; then the lack
// of a match does not prove absence.
func (s *service) readOtherWindows(
	read *untilApplicationRead,
) (*appSnapshot, bool) {
	others := &appSnapshot{App: read.primary.App, capturedAt: read.primary.capturedAt,
		Windows: read.primary.Windows}
	complete := true
	for _, window := range read.otherWindows() {
		elements, ok := s.readUntilWindow(read.request, window.Handle)
		if !ok {
			complete = false
			continue
		}
		others.Elements = append(others.Elements, elements...)
	}
	return others, complete
}

// readUntilWindow reads one more window of the application. It goes to the
// provider directly: readSnapshot would pass the bound window's known refs
// and then read this window a second time without them.
func (s *service) readUntilWindow(
	request axServiceRequest,
	handle string,
) ([]elementRecord, bool) {
	if !validInternalWindowHandle(handle) {
		return nil, false
	}
	request.TargetWindowHandle = handle
	request.Scope = a11yScopeWindow
	request.KnownRefs = nil
	request.SourceRef = ""
	if previous := s.currentSnapshot(request.App); previous != nil {
		applyProjection(&request, previous.projection)
	}
	response, err := s.callProviderAccessibilityOperation(request)
	if err != nil || response == nil {
		return nil, false
	}
	response.prepareForService()
	if !response.OK {
		// A window that closed after the inventory holds nothing.
		return nil, response.FailureFactsComplete &&
			predicateWindowGoneCode(response.FailureCode, response.FailureSubject)
	}
	if response.Snapshot == nil {
		return nil, false
	}
	snapshot, result := s.prepareProviderSnapshot(request, response)
	if result.IsError || snapshot == nil {
		return nil, false
	}
	return snapshot.Elements, true
}

// bindPredicateAppIdentity binds an aN for a read. When the aN was retired,
// for example because the action closed the application's last window, the
// read names the same exact process so the provider can report whether that
// process still exists. The ref itself stays retired.
func (s *service) bindPredicateAppIdentity(request *axServiceRequest) error {
	err := s.bindRuntimeAppIdentity(request)
	if err == nil {
		return nil
	}
	if identity := s.appRefRegistry.ProviderIdentity(request.App); identity != nil {
		request.AppIdentity = identity
		return nil
	}
	return err
}

func predicateWindowGone(result toolCallResult) bool {
	code, _ := result.StructuredContent["failure_code"].(string)
	subject, _ := result.StructuredContent["failure_subject"].(string)
	return predicateWindowGoneCode(code, subject)
}

func predicateWindowGoneCode(code, subject string) bool {
	return code == "stale_window_ref" || code == "stale_window" ||
		(code == "stale_target" && subject == "window")
}

func predicateAppGone(result toolCallResult) bool {
	code, _ := result.StructuredContent["failure_code"].(string)
	subject, _ := result.StructuredContent["failure_subject"].(string)
	complete, _ := result.StructuredContent["failure_facts_complete"].(bool)
	return complete && subject == "app" &&
		(code == "stale_app_ref" || code == "stale_target")
}

// recordUntilWindowRef stores the window an until hint should name: the one
// the predicate read found, else the one other window of the final
// observation that is modal or has focus.
func recordUntilWindowRef(
	structuredContent map[string]any,
	postcondition,
	finalObservation toolCallResult,
) {
	if structuredContent == nil {
		return
	}
	ref, _ := postcondition.StructuredContent["until_window_ref"].(string)
	if ref == "" {
		ref = soleFocusedOtherWindowRef(finalObservation.axProofSnapshot)
	}
	if ref != "" {
		structuredContent["until_window_ref"] = ref
	}
}

// untilWindowHint returns the corrected call for an until that did not hold:
// observe the concrete window that appeared, took focus, or replaced the
// closed one, when the evidence names exactly one.
func untilWindowHint(full map[string]any) string {
	for _, key := range []string{"until_window_ref", "final_observation_window_ref"} {
		if ref, _ := full[key].(string); isShortAXWindowRef(ref) {
			return `{"mode":"observe","window_ref":"` + ref + `"}`
		}
	}
	return ""
}
