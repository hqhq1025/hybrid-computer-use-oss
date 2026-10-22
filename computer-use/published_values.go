package main

import (
	_ "image/jpeg"
	_ "image/png"
)

const maxAXFindPageSize = 50

const (
	treeViewWorking = "working"
	treeViewFull    = "full"
	axScopeWindow   = "window"
	axScopeContent  = "content"
)

var axScopeValues = []string{axScopeWindow, axScopeContent}

const (
	defaultSettleMS = 800
	maxSettleMS     = 3000
)

// Public AX error codes. Internal failure codes stay in telemetry; the model
// sees only these.
const (
	axCodeInvalidArgument  = "invalid_argument"
	axCodeStaleRef         = "stale_ref"
	axCodeWindowChanged    = "window_changed"
	axCodeWaitTimeout      = "wait_timeout"
	axCodeEffectUnknown    = "effect_unknown"
	axCodeAppUnavailable   = "app_unavailable"
	axCodeRuntimeError     = "runtime_error"
	axCodeAmbiguousWindow  = "ambiguous_window"
	axCodeWindowNotFound   = "window_not_found"
	axCodeNoFocusedWindow  = "no_focused_window"
	axModeApps             = "apps"
	axDefaultFindPageSize  = 20
	axDefaultWaitTimeout   = 5000
	axMaxUntilStableMS     = 5000
	axPublicErrorCodeKey   = "public_error_code"
	axPublicErrorPathKey   = "public_error_path"
	axPublicErrorHintKey   = "public_error_hint"
	axPublicErrorDetailKey = "public_error_message"
)

var axActionTypes = []string{
	"click", "type", "keypress", "set_value", "select", "scroll", "activate", "close", "wait",
}

// axFindFilters are the query fields shared by find and a wait step.
var axFindFilters = []string{"role", "name_equals", "name_contains", "text_equals", "text_contains", "state"}

const (
	axModeObserve = "observe"
	axModeFind    = "find"
	axModeAct     = "act"
	// axModeAssert names the internal predicate check that a wait step
	// runs. It is not a public mode.
	axModeAssert = "assert_ax"
)

const guiCodePermissionDenied = "permission_denied"

const guiPermissionHint = "Ask the user to grant the denied system permission to the current Computer Use Provider " +
	"(on macOS: Screen Recording or Accessibility in System Settings > Privacy & Security), " +
	"restart the provider, then take a fresh GUI screenshot before retrying input."

const (
	formatText    = "text"
	formatRecords = "records"
)

// recordsOpRead is the records-only AX mode {"mode":"read","refs":[...]}.
// It has no MCP form: the published schema and the model contract do not
// list it, and the text format rejects it.
const recordsOpRead = "read"
