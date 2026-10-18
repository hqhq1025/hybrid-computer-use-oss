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
}

// axWindowInventory is one complete listing of top-level windows.
type axWindowInventory struct {
	windows        []axDesktopWindow
	infrastructure []string
	text           string
}

// listDesktopWindows asks the provider for every application and its
// top-level windows and issues a wN for each addressable window. The same
// registry issues wN for observed windows, so a window keeps its ref across
// apps, observe, and act.
func (s *service) listDesktopWindows() (*axWindowInventory, toolCallResult) {
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
	inventory := &axWindowInventory{}
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
				lines = append(lines, fmt.Sprintf("-  %s  app=%s %s  (not addressable)",
					strconv.Quote(window.Title), app.Name, appRef))
				continue
			}
			listed++
			entry := axDesktopWindow{
				ref: ref, appRef: appRef, appName: app.Name,
				title: window.Title, active: window.Active, handle: window.Handle,
			}
			inventory.windows = append(inventory.windows, entry)
			lines = append(lines, entry.line())
		}
		if listed == 0 && len(app.Windows) == 0 {
			// A provider without a window listing still reports the app.
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
	if len(lines) == 0 {
		lines = append(lines, "No top-level windows are visible to the accessibility provider.")
	}
	inventory.text = strings.Join(lines, "\n")
	return inventory, toolCallResult{}
}

func (window axDesktopWindow) line() string {
	line := fmt.Sprintf("%s %s  app=%s %s", window.ref, strconv.Quote(window.title), window.appName, window.appRef)
	if window.active {
		line += "  active"
	}
	return line
}

func (s *service) listApps() toolCallResult {
	inventory, failure := s.listDesktopWindows()
	if inventory == nil {
		return failure
	}
	result := textResult(inventory.text, false)
	result.StructuredContent = map[string]any{"window_count": len(inventory.windows)}
	return result
}

// resolveWindowTitle finds the one visible top-level window whose title
// contains title, case-insensitively. It runs once, before any input; the
// returned wN is then verified like any other window ref and is never
// matched by title again.
func (s *service) resolveWindowTitle(title string) (*axDesktopWindow, *axArgError) {
	inventory, failure := s.listDesktopWindows()
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
	inventory, failure := s.listDesktopWindows()
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
