package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// axDesktopWindow is one addressable top-level window from an apps listing.
type axDesktopWindow struct {
	ref     string
	appRef  string
	appName string
	title   string
	active  bool
	handle  string
	// appProcess is the provider identity of the owning application
	// process: its bus name and process generation.
	appProcess [2]string
}

// axWindowInventory contains confirmed windows and any unobserved applications.
type axWindowInventory struct {
	windows []axDesktopWindow
	issues  []ProviderInventoryIssue
	// titles holds every input-target window title, including windows
	// without a ref, so a title wait never misses one.
	titles         []string
	infrastructure []string
	text           string
}

// listDesktopWindows asks the provider for every application and its
// top-level windows and issues a wN for each addressable window. The same
// registry issues wN for observed windows, so a window keeps its ref across
// apps, observe, and act.
func (s *service) listDesktopWindows(allowIncomplete bool) (*axWindowInventory, toolCallResult) {
	response, err := s.callProviderObserveApps(ProviderObserveAppsRequest{
		ProtocolVersion: providerProtocolV2,
	})
	if err != nil {
		var timeout *runtimeTimeoutError
		if errors.As(err, &timeout) {
			result := textResult(err.Error()+"\nAccessibility application enumeration did not answer.", true)
			result.StructuredContent = map[string]any{"failure_code": "runtime_timeout"}
			return nil, result
		}
		return nil, textResult(err.Error(), true)
	}
	if !response.OK {
		return nil, textResult(response.Error, true)
	}
	inventory := &axWindowInventory{issues: response.InventoryIssues}
	issueLines := []string{}
	for _, issue := range inventory.issues {
		subject := "applications"
		if issue.App != "" {
			subject = strconv.Quote(issue.App)
		}
		issueLines = append(issueLines, "Incomplete window inventory for "+subject+": "+issue.Reason)
	}
	if !allowIncomplete && len(issueLines) > 0 {
		result := textResult(strings.Join(issueLines, "\n")+
			"\nWindow absence, unique titles, and focus cannot be inferred from this listing. Use a confirmed wN from AX apps.", true)
		result.StructuredContent = map[string]any{
			"failure_code":       "observation_incomplete",
			"inventory_complete": false,
			"inventory_issues":   inventory.issues,
		}
		return nil, result
	}
	lines := []string{}
	for _, app := range response.Apps {
		if !app.InputCapable {
			inventory.infrastructure = append(inventory.infrastructure, app.Name)
			continue
		}
		appRef := s.appRefRegistry.Acquire(app.Name, app.ProviderBusName, app.ProcessGeneration, "")
		appIdentity := strings.ToLower(strings.TrimSpace(firstNonEmpty(app.BundleIdentifier, app.Name)))
		listed := 0
		for _, window := range app.Windows {
			if !window.InputTarget {
				continue
			}
			inventory.titles = append(inventory.titles, window.Title)
			ref := s.windowRefRegistry.Acquire(
				appIdentity,
				window.Handle,
				selectorHead(firstNonEmpty(window.Role, "window"), window.Title),
				"",
				window.ProviderBusName,
				window.ProviderObjectPath,
				window.ProcessGeneration,
				window.Role,
				window.Title,
				appRef,
			)
			if ref == "" {
				// No strong native handle: the window cannot be verified
				// before input, so it gets no ref.
				lines = append(lines, fmt.Sprintf("-  %s  app=%s  (not addressable)",
					strconv.Quote(window.Title), app.Name))
				continue
			}
			listed++
			entry := axDesktopWindow{
				ref: ref, appRef: appRef, appName: app.Name,
				title: window.Title, active: window.Active, handle: window.Handle,
				appProcess: [2]string{app.ProviderBusName, app.ProcessGeneration},
			}
			inventory.windows = append(inventory.windows, entry)
			lines = append(lines, entry.line())
		}
		if listed == 0 && len(app.Windows) == 0 {
			// A provider without a window listing still reports the app. It
			// has no wN, so the line keeps the aN as its only exact handle.
			line := strings.TrimSpace(app.DisplayText)
			if line == "" {
				line = app.Name
			}
			lines = append(lines, fmt.Sprintf("%s app=%s %s", line, app.Name, appRef))
		}
	}
	if len(inventory.infrastructure) > 0 {
		sort.Strings(inventory.infrastructure)
		lines = append(lines, "(not input targets: "+strings.Join(inventory.infrastructure, ", ")+")")
	}
	// The provider says when the listing is incomplete, for example when an
	// application does not answer; a missing window is not proof of absence.
	lines = append(lines, response.Notes...)
	lines = append(lines, issueLines...)
	if len(lines) == 0 {
		lines = append(lines, "No top-level windows are visible to the accessibility provider.")
	}
	inventory.text = strings.Join(lines, "\n")
	return inventory, toolCallResult{}
}

// line shows the app by name only: the wN already names the window and its
// app, and models never passed the aN (0 of 517 recorded AX calls).
func (window axDesktopWindow) line() string {
	line := fmt.Sprintf("%s %s  app=%s", window.ref, strconv.Quote(window.title), window.appName)
	if window.active {
		line += "  active"
	}
	return line
}

func (s *service) listApps() toolCallResult {
	inventory, failure := s.listDesktopWindows(true)
	if inventory == nil {
		return failure
	}
	s.lastInventory = inventory
	result := textResult(inventory.text, false)
	result.StructuredContent = map[string]any{"window_count": len(inventory.windows)}
	if len(inventory.issues) > 0 {
		result.StructuredContent["inventory_complete"] = false
		result.StructuredContent["inventory_issues"] = inventory.issues
	}
	return result
}

// describeAmbiguousApp completes an app-name ambiguity failure. The
// provider names each matching application process; this lists their
// windows with the wN that apps issues, so the next call can pick one window
// instead of guessing another name. Nothing was sent.
func (s *service) describeAmbiguousApp(result *toolCallResult, candidates []ProviderTargetCandidate) {
	result.StructuredContent[axPublicErrorPathKey] = "app"
	result.StructuredContent[axPublicErrorHintKey] = `{"mode":"apps"}`
	processes := map[[2]string]bool{}
	for _, candidate := range candidates {
		if candidate.ProviderBusName != "" && candidate.ProcessGeneration != "" {
			processes[[2]string{candidate.ProviderBusName, candidate.ProcessGeneration}] = true
		}
	}
	if len(processes) == 0 {
		return
	}
	inventory, _ := s.listDesktopWindows(false)
	if inventory == nil {
		return
	}
	lines := []string{}
	first := ""
	for _, window := range inventory.windows {
		if !processes[window.appProcess] {
			continue
		}
		if first == "" {
			first = window.ref
		}
		lines = append(lines, window.line())
	}
	if first == "" {
		return
	}
	result.Content[0].Text += "\nWindows of the matching applications; pass one wN as window_ref:\n" +
		strings.Join(lines, "\n")
	result.StructuredContent[axPublicErrorHintKey] = `{"mode":"observe","window_ref":"` + first + `"}`
}

// resolveWindowTitle finds the one visible top-level window whose title
// contains title, case-insensitively. It runs once, before any input; the
// returned wN is then verified like any other window ref and is never
// matched by title again.
func (s *service) resolveWindowTitle(title string) (*axDesktopWindow, *axArgError) {
	inventory, failure := s.listDesktopWindows(false)
	if inventory == nil {
		return nil, &axArgError{
			code:    axCodeRuntimeError,
			path:    "title",
			message: "the window list could not be read: " + firstLine(resultText(failure)),
		}
	}
	matches := []axDesktopWindow{}
	for _, window := range inventory.windows {
		if containsFold(window.title, title) {
			matches = append(matches, window)
		}
	}
	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		return nil, &axArgError{
			code:    axCodeWindowNotFound,
			path:    "title",
			message: "no visible window title contains " + strconv.Quote(title) + ". Windows:\n" + inventory.text,
			hint:    `{"type":"activate","ref":"wN"}`,
		}
	default:
		lines := make([]string, 0, len(matches))
		for _, window := range matches {
			lines = append(lines, window.line())
		}
		return nil, &axArgError{
			code:    axCodeAmbiguousWindow,
			path:    "title",
			message: fmt.Sprintf("%d windows match title %s; use one wN:\n%s", len(matches), strconv.Quote(title), strings.Join(lines, "\n")),
			hint:    `{"type":"activate","ref":"` + matches[0].ref + `"}`,
		}
	}
}

// resolveFocusedWindow returns the window that holds keyboard focus now.
func (s *service) resolveFocusedWindow() (*axDesktopWindow, *axArgError) {
	inventory, failure := s.listDesktopWindows(false)
	if inventory == nil {
		return nil, &axArgError{
			code:    axCodeRuntimeError,
			message: "the window list could not be read: " + firstLine(resultText(failure)),
		}
	}
	for index := range inventory.windows {
		if inventory.windows[index].active {
			return &inventory.windows[index], nil
		}
	}
	return nil, &axArgError{
		code: axCodeNoFocusedWindow,
		message: "no application window has keyboard focus, so keys or text without ref have no target. " +
			"Start the batch with activate. Windows:\n" + inventory.text,
		hint: `{"type":"activate","title":"window title"}`,
	}
}
