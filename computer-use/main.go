package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var version = "0.3.0"

const (
	publicToolAX  = "AX"
	publicToolGUI = "GUI"
)

var clickMethodValues = []string{"auto", "accessibility", "app_post", "sky_click", "global"}

const (
	guiMaxActions       = 64
	guiMaxScrollDelta   = 2000
	guiMaxDragPoints    = 256
	guiMaxKeys          = 8
	guiMaxKeyLength     = 64
	guiMaxTextLength    = 10000
	guiMaxTotalWait     = 20
	guiMaxSettleMS      = 3000
	a11yBatchMaxActions = 32
	a11yBatchMaxText    = 20000
	maxClickCount       = 10
	maxScrollPages      = 100
	maxVerifyTimeout    = 60000
	maxActionText       = 10000
	maxMCPMessageBytes  = 1 << 20
	maxDeliveredTrees   = 512
)

const (
	mcpProtocolLegacy     = "2025-03-26"
	mcpProtocolStructured = "2025-06-18"
)

const serverInstructions = "" +
	"Control the already-running desktop with two complementary tools. AX is " +
	"app-scoped semantic observation and action; GUI is full-desktop visual observation " +
	"and absolute-pixel input. Use AX for named controls and GUI for pixels or content " +
	"absent from the accessibility tree. AX find uses explicit equals/contains fields. " +
	"Copy returned rN/wN refs exactly; never invent placeholders. A targeted ref owns " +
	"its app and window, so omit app, app_ref and window_ref. Use window_ref only when " +
	"every action is untargeted keyboard input. Batch only actions valid in one UI " +
	"generation; split after likely dialog, window or navigation changes. Window " +
	"activate/close uses wN and close must be last. AX action acceptance and visual " +
	"change are evidence, not proof of task success; AX cannot prove files, persistence, " +
	"network state or pixels. GUI keyboard/text uses current focus. AX keyboard/pointer " +
	"fallbacks use global desktop focus, not background input. After GUI mutation, " +
	"refresh AX before reusing semantic state. AX target-window screenshot coordinates " +
	"are never GUI coordinates; GUI coordinates use the exact delivered full-desktop image."

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Annotations map[string]any `json:"annotations,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

const axToolProjectionContract = ""

type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type toolCallResult struct {
	Content           []contentItem  `json:"content"`
	IsError           bool           `json:"isError"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	axProofSnapshot   *appSnapshot
	axProofNotes      []string
}

func textResult(text string, isError bool) toolCallResult {
	return toolCallResult{Content: []contentItem{{Type: "text", Text: text}}, IsError: isError}
}

func (r toolCallResult) withEvidenceScope(scope string) toolCallResult {
	if r.StructuredContent == nil {
		r.StructuredContent = map[string]any{}
	}
	if r.IsError {
		r.StructuredContent["evidence_scope"] = "error_only"
	} else {
		r.StructuredContent["evidence_scope"] = scope
	}
	// Generic positive success bits are too easy to upgrade into "the requested
	// task effect happened". JSON-RPC already proves the call returned; narrower
	// fields report action attempt, input emission and predicate evidence.
	delete(r.StructuredContent, "operation_completed")
	delete(r.StructuredContent, "tool_call_completed")
	delete(r.StructuredContent, "tool_execution_succeeded")
	r.StructuredContent["task_effect_verified"] = false
	return r
}

func (r toolCallResult) withDefaultEvidenceScope(scope string) toolCallResult {
	if r.StructuredContent != nil {
		if existing, ok := r.StructuredContent["evidence_scope"].(string); ok && existing != "" {
			return r
		}
	}
	return r.withEvidenceScope(scope)
}

type appDescriptor struct {
	Name              string `json:"name"`
	BundleIdentifier  string `json:"bundleIdentifier,omitempty"`
	PID               int    `json:"pid"`
	ProviderBusName   string `json:"providerBusName,omitempty"`
	ProcessGeneration string `json:"processGeneration,omitempty"`
	DisplayText       string `json:"displayText,omitempty"`
	InputCapable      bool   `json:"inputCapable"`
}

type frame struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type windowSelectionCandidate struct {
	Index    int    `json:"index"`
	Role     string `json:"role,omitempty"`
	Title    string `json:"title,omitempty"`
	Active   bool   `json:"active,omitempty"`
	Showing  bool   `json:"showing,omitempty"`
	Visible  bool   `json:"visible,omitempty"`
	Modal    bool   `json:"modal,omitempty"`
	Bounds   *frame `json:"bounds,omitempty"`
	Selected bool   `json:"selected,omitempty"`
}

type windowSelection struct {
	Reason         string                     `json:"reason,omitempty"`
	CandidateCount int                        `json:"candidateCount,omitempty"`
	Candidates     []windowSelectionCandidate `json:"candidates,omitempty"`
}

func (f frame) renderedLocalFrame() string {
	return fmt.Sprintf("{{x: %.0f, y: %.0f, width: %.0f, height: %.0f}}", f.X, f.Y, f.Width, f.Height)
}

type elementRecord struct {
	Index                           int                           `json:"index"`
	RuntimeID                       []int                         `json:"runtimeId,omitempty"`
	RuntimeIDAliases                [][]int                       `json:"runtimeAliases,omitempty"`
	AutomationID                    string                        `json:"automationId,omitempty"`
	ProviderBusName                 string                        `json:"providerBusName,omitempty"`
	ProviderObjectPath              string                        `json:"providerObjectPath,omitempty"`
	ProcessGeneration               string                        `json:"processGeneration,omitempty"`
	ProviderIdentityScope           string                        `json:"providerIdentityScope,omitempty"`
	ProviderWindowBusName           string                        `json:"providerWindowBusName,omitempty"`
	ProviderWindowObjectPath        string                        `json:"providerWindowObjectPath,omitempty"`
	ProviderWindowProcessGeneration string                        `json:"providerWindowProcessGeneration,omitempty"`
	ProviderWindowRole              string                        `json:"providerWindowRole,omitempty"`
	ProviderWindowName              string                        `json:"providerWindowName,omitempty"`
	Showing                         *bool                         `json:"showing,omitempty"`
	Visible                         *bool                         `json:"visible,omitempty"`
	NameHash                        string                        `json:"nameHash,omitempty"`
	Name                            string                        `json:"name,omitempty"`
	ControlType                     string                        `json:"controlType,omitempty"`
	LocalizedControlType            string                        `json:"localizedControlType,omitempty"`
	ClassName                       string                        `json:"className,omitempty"`
	Value                           string                        `json:"value,omitempty"`
	NativeWindowHandle              int64                         `json:"nativeWindowHandle,omitempty"`
	Frame                           *frame                        `json:"frame,omitempty"`
	Actions                         []string                      `json:"actions,omitempty"`
	States                          string                        `json:"states,omitempty"`
	Description                     string                        `json:"description,omitempty"`
	TextAttributes                  map[string]string             `json:"textAttributes,omitempty"`
	Placeholder                     string                        `json:"placeholder,omitempty"`
	Relations                       map[string][]axRelationTarget `json:"relations,omitempty"`
	RelationReadComplete            bool                          `json:"relationReadComplete,omitempty"`
	NameSource                      string                        `json:"nameSource,omitempty"`
	FindMatchedField                string                        `json:"-"`
	FindMatchedText                 string                        `json:"-"`
}

type axRelationTarget struct {
	Role               string `json:"role,omitempty"`
	Name               string `json:"name,omitempty"`
	ProviderBusName    string `json:"providerBusName,omitempty"`
	ProviderObjectPath string `json:"providerObjectPath,omitempty"`
	ProcessGeneration  string `json:"processGeneration,omitempty"`
}

type axProviderAnnotation struct {
	ParentRuntimeID []int  `json:"parentRuntimeId,omitempty"`
	Kind            string `json:"kind"`
	Count           int    `json:"count,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// elementRef is an element's stable ref together with the fields that
// invalidate it. As in Playwright's ariaSnapshot.ts, a change of role or name
// issues a new ref, because the element is no longer the same object.
type elementRef struct {
	Index    int    `json:"index"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	NameHash string `json:"nameHash,omitempty"`
}

// busySignal is evidence that the application is still working, computed by
// busy_signal in the Linux provider. Without it, an unfinished asynchronous
// operation and budget-truncated output look identical in the returned text.
type busySignal struct {
	WindowBusy       bool `json:"windowBusy"`
	ProgressElements int  `json:"progressElements"`
}

func (b *busySignal) busy() bool {
	return b != nil && (b.WindowBusy || b.ProgressElements > 0)
}

type resourceFingerprint struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtimeNs"`
}

type openFileFingerprint struct {
	FD      int    `json:"fd"`
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtimeNs"`
	Deleted bool   `json:"deleted"`
}

type resourceCandidate struct {
	Path                 string                `json:"path"`
	Kind                 string                `json:"kind"`
	Sources              []string              `json:"sources"`
	Deleted              bool                  `json:"deleted"`
	OpenFileFingerprint  *openFileFingerprint  `json:"openFileFingerprint,omitempty"`
	OpenFileFingerprints []openFileFingerprint `json:"openFileFingerprints,omitempty"`
}

type documentState struct {
	IdentityStatus       string                `json:"identityStatus"`
	IdentityKind         string                `json:"identityKind"`
	IdentityValue        string                `json:"identityValue,omitempty"`
	IdentityBasis        []string              `json:"identityBasis,omitempty"`
	Candidates           []resourceCandidate   `json:"candidates,omitempty"`
	Dirty                *bool                 `json:"dirty,omitempty"`
	DirtyBasis           string                `json:"dirtyBasis,omitempty"`
	TitleModifiedMarker  string                `json:"titleModifiedMarker,omitempty"`
	DiskStatus           string                `json:"diskStatus"`
	PathFingerprint      *resourceFingerprint  `json:"pathFingerprint,omitempty"`
	OpenFileFingerprint  *openFileFingerprint  `json:"openFileFingerprint,omitempty"`
	OpenFileFingerprints []openFileFingerprint `json:"openFileFingerprints,omitempty"`
	ScanDeadlineExceeded bool                  `json:"scanDeadlineExceeded,omitempty"`
	ScanIncomplete       bool                  `json:"scanIncomplete,omitempty"`
}

type appSnapshot struct {
	App                 appDescriptor          `json:"app"`
	WindowTitle         string                 `json:"windowTitle,omitempty"`
	WindowHandle        string                 `json:"windowHandle,omitempty"`
	WindowAbsent        bool                   `json:"windowAbsent,omitempty"`
	ClosedWindowHandle  string                 `json:"closedWindowHandle,omitempty"`
	Windows             []topLevelWindow       `json:"windows,omitempty"`
	WindowSelection     *windowSelection       `json:"windowSelection,omitempty"`
	SnapshotID          string                 `json:"snapshotId,omitempty"`
	SampledAt           string                 `json:"sampledAt,omitempty"`
	Busy                *busySignal            `json:"busy,omitempty"`
	DocumentState       *documentState         `json:"documentState,omitempty"`
	WindowBounds        *frame                 `json:"windowBounds,omitempty"`
	ScreenshotPNGBase64 string                 `json:"screenshotPngBase64,omitempty"`
	ScreenshotMimeType  string                 `json:"screenshotMimeType,omitempty"`
	ScreenshotEncoding  string                 `json:"screenshotEncoding,omitempty"`
	CoordinateSpace     string                 `json:"coordinateSpace,omitempty"`
	FrameID             string                 `json:"frameId,omitempty"`
	ContentFrameID      string                 `json:"contentFrameId,omitempty"`
	DesktopLayout       map[string]any         `json:"desktopLayout,omitempty"`
	Width               int                    `json:"width,omitempty"`
	Height              int                    `json:"height,omitempty"`
	TreeLines           []string               `json:"treeLines,omitempty"`
	FocusedSummary      string                 `json:"focusedSummary,omitempty"`
	SelectedText        string                 `json:"selectedText,omitempty"`
	Elements            []elementRecord        `json:"elements,omitempty"`
	Refs                map[string]elementRef  `json:"refs,omitempty"`
	Annotations         []axProviderAnnotation `json:"annotations,omitempty"`

	// The fields below are set by the Go service when the snapshot is cached;
	// the provider does not send them. Snapshot age matters on the desktop:
	// unlike DOM changes in a browser, desktop UI changes raise no observable
	// event, so a snapshot can be stale for tens of seconds unnoticed.
	capturedAt time.Time
	sourceApp  string
	// Relation to the previous snapshot. The provider captures each snapshot
	// independently in a one-shot process, so only this side, which holds the
	// history, can tell whether a snapshot matches the previous one.
	comparedToPrevious        bool
	identicalToPrevious       bool
	previousSnapshotID        string
	previousHandle            string
	projection                snapshotProjection
	rawTreeLines              []string
	baselineProjectionLines   []string
	serializerOmitted         int
	serializerBranches        int
	scope                     string
	scopeRoot                 *elementRecord
	scopedCanonicalCount      int
	observationRoot           *elementRecord
	observationRootMode       string
	rootedCanonicalCount      int
	contentRoot               *elementRecord
	contentCanonicalCount     int
	contentRevision           string
	shortRefs                 map[string]string
	axSnapshot                *AXSnapshotV1
	shadowAST                 AXShadowStats
	actionExecution           *RuntimeActionEvidence
	deliveryOnly              bool
	publicAppRef              string
	publicWindowRef           string
	externalDesktopTransition bool
}

type topLevelWindow struct {
	Title                     string `json:"title,omitempty"`
	Role                      string `json:"role,omitempty"`
	Handle                    string `json:"handle,omitempty"`
	ProviderBusName           string `json:"providerBusName,omitempty"`
	ProviderObjectPath        string `json:"providerObjectPath,omitempty"`
	ProviderProcessGeneration string `json:"providerProcessGeneration,omitempty"`
	Selected                  bool   `json:"selected,omitempty"`
	Active                    bool   `json:"active,omitempty"`
	Modal                     bool   `json:"modal,omitempty"`
	Showing                   bool   `json:"showing,omitempty"`
	publicRef                 string
}

type snapshotProjection struct {
	TextLimit    any
	MaxTreeNodes int
	MaxTreeDepth int
	Prune        *bool
	Boxes        *bool
	View         string
	Scope        string
	Root         string
}

const (
	treeViewWorking  = "working"
	treeViewFull     = "full"
	a11yScopeWindow  = "window"
	a11yScopeContent = "content"
)

var treeViewValues = []string{treeViewWorking, treeViewFull}
var a11yScopeValues = []string{a11yScopeWindow, a11yScopeContent}

func normalizedTreeView(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case treeViewFull:
		return treeViewFull
	default:
		return treeViewWorking
	}
}

func parseTreeView(args map[string]any) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(optionalString(args, "view")))
	if raw == "" {
		return treeViewWorking, nil
	}
	for _, value := range treeViewValues {
		if raw == value {
			return value, nil
		}
	}
	return "", fmt.Errorf(
		"invalid view %q. Valid values are %s; omit it for %q",
		optionalString(args, "view"),
		strings.Join(treeViewValues, ", "),
		treeViewWorking,
	)
}

func normalizedA11yScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case a11yScopeContent:
		return a11yScopeContent
	default:
		return a11yScopeWindow
	}
}

func parseA11yScope(args map[string]any) (string, error) {
	raw := strings.ToLower(strings.TrimSpace(optionalString(args, "scope")))
	if raw == "" {
		return a11yScopeWindow, nil
	}
	for _, value := range a11yScopeValues {
		if raw == value {
			return value, nil
		}
	}
	return "", fmt.Errorf(
		"invalid scope %q. Valid values are %s; omit it for %q",
		optionalString(args, "scope"),
		strings.Join(a11yScopeValues, ", "),
		a11yScopeWindow,
	)
}

func parseObservationRoot(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New(
			`must be {"focused":true}, {"index":N}, {"ref":"rN"}, or {"selector":{...}}`,
		)
	}
	if focused, present := root["focused"]; present {
		if len(root) != 1 {
			return nil, errors.New(
				`focused root must contain only {"focused":true}`,
			)
		}
		value, ok := focused.(bool)
		if !ok || !value {
			return nil, errors.New("focused must be true")
		}
		return map[string]any{"focused": true}, nil
	}
	if rawRef, present := root["ref"]; present {
		if ref, ok := rawRef.(string); ok && isShortAXWindowRef(ref) {
			return nil, errors.New(
				"root.ref must identify an element with rN; wN is only for window activate/close",
			)
		}
	}
	if _, _, err := a11yTargetAddress(
		map[string]any{"target": root},
	); err != nil {
		return nil, err
	}
	copy := make(map[string]any, len(root))
	for key, value := range root {
		copy[key] = value
	}
	return copy, nil
}

func observationRootKey(root map[string]any) string {
	if len(root) == 0 {
		return ""
	}
	payload, err := json.Marshal(root)
	if err != nil {
		return ""
	}
	return string(payload)
}

func projectionFromRequest(request axServiceRequest) snapshotProjection {
	return snapshotProjection{
		TextLimit:    request.TextLimit,
		MaxTreeNodes: request.MaxTreeNodes,
		MaxTreeDepth: request.MaxTreeDepth,
		Prune:        request.Prune,
		Boxes:        request.Boxes,
		View:         normalizedTreeView(request.TreeView),
		Scope:        normalizedA11yScope(request.Scope),
		Root:         observationRootKey(request.ObservationRoot),
	}
}

func applyProjection(request *axServiceRequest, projection snapshotProjection) {
	if request == nil {
		return
	}
	request.TextLimit = projection.TextLimit
	request.MaxTreeNodes = projection.MaxTreeNodes
	request.MaxTreeDepth = projection.MaxTreeDepth
	request.Prune = projection.Prune
	request.Boxes = projection.Boxes
	request.TreeView = normalizedTreeView(projection.View)
	// Scope is request-local. Persisting content scope into later action reads
	// would make a newly opened menu or dialog disappear from the final
	// observation. Explicit observe/find calls must request it again.
	// ObservationRoot is request-local for the same reason: action readback
	// must not inherit a previous diagnostic subtree.
}

func (s *appSnapshot) renderedText() string {
	if s == nil {
		return ""
	}
	title := s.WindowTitle
	if strings.TrimSpace(title) == "" {
		title = s.App.Name
	}
	windowLine := fmt.Sprintf("window: %q", title)
	if s.WindowAbsent {
		windowLine = "window: null"
	} else if s.publicWindowRef != "" {
		windowLine = fmt.Sprintf(
			"window: %q [ref=%s]", title, s.publicWindowRef,
		)
	} else if s.WindowHandle != "" {
		windowLine = fmt.Sprintf(
			"window: %q [window-action-ref=unavailable; rN actions need no app_ref/window_ref]", title,
		)
	}
	lines := []string{}
	if s.publicAppRef != "" {
		appLine := fmt.Sprintf(
			"app: %q [ref=%s]", s.App.Name, s.publicAppRef,
		)
		lines = append(lines, appLine)
	}
	lines = append(lines, windowLine)
	if len(s.Windows) > 1 {
		lines = append(lines, "windows:")
		lines = append(lines, renderedWindowInventory(s.Windows)...)
	}
	if normalizedA11yScope(s.scope) == a11yScopeContent && s.scopeRoot != nil {
		lines = append(lines, fmt.Sprintf(
			"scope: content root=%s %q",
			canonicalRole(s.scopeRoot),
			s.scopeRoot.Name,
		))
	}
	if s.observationRoot != nil {
		lines = append(lines, fmt.Sprintf(
			"root: mode=%s %s %q",
			s.observationRootMode,
			canonicalRole(s.observationRoot),
			s.observationRoot.Name,
		))
	}
	lines = append(lines, s.TreeLines...)
	return strings.Join(lines, "\n")
}

// outputMode reports whether large outputs are written to files ("file") or
// returned inline ("stdout"). File mode mirrors playwright-mcp's --output-mode
// option: instead of compressing a large output, it writes the output to a
// file and returns only the path, and the agent reads the detail when needed.
// Desktop accessibility trees can reach tens of thousands of characters while
// the agent usually needs one or two elements.
//
// The default is stdout for two reasons:
//  1. File output turns one call into two, a net loss for small trees.
//  2. File output requires the agent to read local files. Restricted clients
//     may not expose them, and the tree would then be unreachable, so only
//     the deployment can enable it.
func outputMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("COMPUTER_USE_OUTPUT_MODE")))
	if mode == "file" {
		return "file"
	}
	return "stdout"
}

func outputDir() string {
	if dir := strings.TrimSpace(os.Getenv("COMPUTER_USE_OUTPUT_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), "computer-use")
}

// outputThreshold is the size, in characters, below which output is never
// written to a file: for small trees, turning one call into two is a net loss.
func outputThreshold() int {
	if raw := strings.TrimSpace(os.Getenv("COMPUTER_USE_OUTPUT_THRESHOLD")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return 4000
}

// spillToFile writes large text to the output directory and returns the
// replacement text shown to the agent. It returns "" when file output does not
// apply or the write fails; a spill failure must never fail the tool, so the
// caller then returns the text inline.
func spillToFile(kind, text string) string {
	if outputMode() != "file" || len(text) < outputThreshold() {
		return ""
	}
	dir := outputDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	name := fmt.Sprintf("%s-%d.txt", kind, time.Now().UnixNano())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return ""
	}
	head := text
	if idx := strings.Index(head, "\n"); idx >= 0 {
		if second := strings.Index(head[idx+1:], "\n"); second >= 0 {
			head = head[:idx+1+second]
		}
	}
	return fmt.Sprintf("%s\n\n[%s written to %s — %d characters, %d lines. "+
		"Read that file when you need the detail; it is the same content this "+
		"response would otherwise have inlined. If you have no way to read files, "+
		"ask the operator to unset COMPUTER_USE_OUTPUT_MODE.]",
		head, kind, path, len(text), strings.Count(text, "\n")+1)
}

func (s *appSnapshot) result() toolCallResult {
	return s.resultWithNotes(nil)
}

// resultWithTreeOverride renders the given lines in place of TreeLines, for
// incremental views.
func (s *appSnapshot) resultWithTreeOverride(notes, lines []string) toolCallResult {
	if s == nil {
		return s.resultWithNotes(notes)
	}
	clone := *s
	clone.TreeLines = lines
	return clone.resultWithNotes(notes)
}

// resultWithNotes puts notes on what the action actually did, and whether its
// result was confirmed, before the accessibility tree. A new tree alone is
// easily misread as "the action succeeded", but it is only a snapshot.
func (s *appSnapshot) resultWithNotes(notes []string) toolCallResult {
	return s.textWithNotes(s.renderedText(), notes)
}

// notesOnlyResult returns only diagnostic notes, without a tree or screenshot.
//
// Most observation cost is a by-product of actions: re-rendering the full tree
// and attaching a screenshot after every action can cost more than an explicit
// observation, and a caller chaining several known actions does not read the
// intermediate trees.
//
// The tree is not dropped by default, because the post-action tree is the
// evidence of whether the step took effect; the caller must opt in. The notes
// still report the resolved element, visual change evidence and any modal
// dialog; only the complete current state is omitted.
func (s *appSnapshot) notesOnlyResult(notes []string) toolCallResult {
	notes = modelFacingAXNotes(notes)
	lines := make([]string, 0, len(notes)+1)
	for _, note := range notes {
		lines = append(lines, "Note: "+note)
	}
	lines = append(lines, "",
		"(observe=\"none\": no final tree or screenshot was returned. Existing rN "+
			"refs remain usable only while their logical "+
			"targets remain valid. Removed or replaced targets become stale; use "+
			"AX mode=observe or find to obtain a fresh ref.)")
	return toolCallResult{Content: []contentItem{
		{Type: "text", Text: capInline(strings.Join(lines, "\n"))},
	}}
}

// withoutScreenshot returns a copy of the snapshot without the image, for
// observation modes that return only the tree. It copies rather than clearing
// in place because the cached snapshot remains the before-state for the next
// action.
func (s *appSnapshot) withoutScreenshot() *appSnapshot {
	if s == nil {
		return nil
	}
	clone := *s
	clone.ScreenshotPNGBase64 = ""
	clone.ScreenshotMimeType = ""
	clone.ScreenshotEncoding = ""
	return &clone
}

func (s *appSnapshot) screenshotMimeType() string {
	if s != nil && strings.TrimSpace(s.ScreenshotMimeType) != "" {
		return s.ScreenshotMimeType
	}
	return "image/png"
}

// screenshotOnlyResult returns only pixels and evidence notes, without the tree.
//
// Formatting changes and file state often never reach the accessibility tree:
// right-aligning text in LibreOffice Impress or saving with Ctrl+S can leave
// the tree bytes unchanged. The tree is then pure overhead and the image is
// the only evidence.
func (s *appSnapshot) screenshotOnlyResult(notes []string) toolCallResult {
	notes = modelFacingAXNotes(notes)
	lines := make([]string, 0, len(notes)+1)
	for _, note := range notes {
		lines = append(lines, "Note: "+note)
	}
	if s == nil || s.ScreenshotPNGBase64 == "" {
		lines = append(lines, "",
			"(observe=\"screenshot\": no image came back for this window. On GNOME "+
				"Wayland the capture can return blank. The tree was refreshed "+
				"server-side. Existing rN refs remain usable only while their logical "+
				"targets remain valid; use AX mode=observe or find after a stale ref.)")
		return toolCallResult{Content: []contentItem{
			{Type: "text", Text: capInline(strings.Join(lines, "\n"))},
		}}
	}
	lines = append(lines, "",
		"(observe=\"screenshot\": the tree was refreshed server-side but not "+
			"returned. Existing rN refs remain usable only while their logical targets "+
			"remain valid; use AX mode=observe or find after a stale ref.)")
	return toolCallResult{Content: []contentItem{
		{Type: "text", Text: capInline(strings.Join(lines, "\n"))},
		{Type: "image", Data: s.ScreenshotPNGBase64, MimeType: s.screenshotMimeType()},
	}}
}

// textWithNotes is the shared output path for trees and other text: notes
// first, then the body, spilled to a file above the threshold and capped
// inline. find results must use the same path, or a query with hundreds of
// matches would reproduce the oversized-output problem.
func (s *appSnapshot) textWithNotes(text string, notes []string) toolCallResult {
	notes = modelFacingAXNotes(notes)
	if len(notes) > 0 {
		lines := make([]string, 0, len(notes)+1)
		for _, note := range notes {
			lines = append(lines, "Note: "+note)
		}
		lines = append(lines, "", text)
		text = strings.Join(lines, "\n")
	}
	if spilled := spillToFile("snapshot", text); spilled != "" {
		text = spilled
	}
	text = capInline(text)
	result := toolCallResult{
		Content: []contentItem{{Type: "text", Text: text}},
	}
	if s != nil && s.ScreenshotPNGBase64 != "" {
		result.Content = append(result.Content, contentItem{
			Type:     "image",
			Data:     s.ScreenshotPNGBase64,
			MimeType: s.screenshotMimeType(),
		})
	}
	return result
}

func providerAliasesForCanonicalRole(role string) []string {
	canonical := CanonicalRole(strings.ToLower(strings.TrimSpace(role)))
	candidates := []string{string(canonical)}
	for _, descriptor := range roleOntology {
		if descriptor.ModelRole != canonical {
			continue
		}
		candidates = append(candidates, descriptor.ProviderAliases...)
	}
	if canonical == "gridcell" {
		candidates = append(candidates, "table cell")
	}
	aliases := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		aliases = append(aliases, candidate)
	}
	return aliases
}

type textLimit struct {
	max   bool
	count int
}

func (limit textLimit) runtimeValue() any {
	if limit.max {
		return "max"
	}
	return limit.count
}

type failedObservationEntry struct {
	query        string
	windowHandle string
	aliases      map[string]bool
}

type observationLedger struct {
	lastComplete map[string]*appSnapshot
	lastFullTree map[string]bool
}

type service struct {
	backend                   PlatformBackend
	snapshots                 map[string]*appSnapshot
	guiSnapshot               *appSnapshot
	guiLeaseState             string
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
// This follows Playwright's error-context.md, which saves a page snapshot with
// each failure because a one-line error cannot be diagnosed afterwards. On the
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
			result.StructuredContent["preflight_only"] = true
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

func enforceAXImageOutputPolicy(
	name string,
	args map[string]any,
	result toolCallResult,
) toolCallResult {
	if internalToolName(name) != "a11y" {
		return result
	}
	if axRequestExplicitlyIncludesScreenshot(args) {
		return result
	}

	content := make([]contentItem, 0, len(result.Content))
	for _, item := range result.Content {
		if item.Type != "image" {
			content = append(content, item)
		}
	}
	result.Content = content
	return result
}

func axRequestExplicitlyIncludesScreenshot(args map[string]any) bool {
	mode, err := parseA11yMode(args)
	if err != nil {
		return false
	}
	switch mode {
	case a11yModeObserve:
		target := defaultString(
			strings.ToLower(strings.TrimSpace(
				optionalString(args, "observation_target"),
			)),
			"app",
		)
		if target != "app" {
			return false
		}
		obs, err := parseObservation(args)
		if err != nil {
			return false
		}
		return obs.resolvedMode() == observeScreenshot ||
			obs.resolvedMode() == observeFull
	case a11yModeAct:
		obs, err := parseObservation(args)
		if err != nil {
			return false
		}
		return obs.resolvedMode() == observeScreenshot
	default:
		return false
	}
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

func structuredContentEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(
		os.Getenv("COMPUTER_USE_STRUCTURED_CONTENT"),
	)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func withTextEvidencePrefix(
	result toolCallResult,
	prefix string,
) toolCallResult {
	if len(result.StructuredContent) == 0 {
		return result
	}
	visible := modelVisibleEvidence(result.StructuredContent)
	if len(visible) == 0 {
		return result
	}
	payload, err := json.Marshal(visible)
	if err != nil {
		return result
	}
	result.Content = append(
		[]contentItem{{
			Type: "text",
			Text: prefix + string(payload),
		}},
		result.Content...,
	)
	return result
}

var modelPrivateEvidenceKeys = map[string]bool{
	"content_revision":                        true,
	"content_revision_before":                 true,
	"content_revision_after":                  true,
	"snapshot_id":                             true,
	"immediate_snapshot_id":                   true,
	"final_snapshot_id":                       true,
	"observed_snapshot_id":                    true,
	"window_identity_stabilization_attempted": true,
	"window_identity_stabilization_trigger":   true,
}

func modelVisibleEvidence(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		if modelPrivateEvidenceKeys[key] {
			continue
		}
		out[key] = value
	}
	return out
}

func withTextEvidence(result toolCallResult) toolCallResult {
	return withTextEvidencePrefix(result, "[tool-evidence] ")
}

func withAXTextEvidence(result toolCallResult) toolCallResult {
	return withTextEvidencePrefix(result, "[ax] ")
}

func intEvidence(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func nonDefaultStringEvidence(
	target map[string]any,
	source map[string]any,
	key string,
	defaults ...string,
) {
	raw, ok := source[key]
	if !ok {
		return
	}
	value := strings.TrimSpace(fmt.Sprint(raw))
	if value == "" {
		return
	}
	for _, defaultValue := range defaults {
		if value == defaultValue {
			return
		}
	}
	target[key] = value
}

func compactTriState(value any) any {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "true":
		return true
	case "false":
		return false
	case "unknown", "", "<nil>":
		return "unknown"
	default:
		return value
	}
}

func boolEvidenceIfTrue(
	target map[string]any,
	source map[string]any,
	key string,
) {
	if value, ok := source[key].(bool); ok && value {
		target[key] = true
	}
}

func compactAXBatchResult(value any) map[string]any {
	full, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	compact := map[string]any{}
	failedIndex, failed := full["failed_action_index"]
	guardFailedIndex, guardFailed := full["guard_failed_action_index"]
	rejected, _ := full["rejected_before_first_action"].(bool)
	partial, _ := full["partial_sequence"].(bool)
	if failed && failedIndex != nil ||
		guardFailed && guardFailedIndex != nil ||
		rejected || partial {
		for _, key := range []string{
			"actions_requested",
			"actions_completed_without_call_error",
			"failed_action_index",
			"failed_action_type",
			"guard_failed_action_index",
			"guard_failed_action_number",
			"guard_failed_action_type",
			"stopped_before_action_index",
			"action_postcondition",
		} {
			if item, present := full[key]; present && item != nil {
				compact[key] = item
			}
		}
		if partial {
			compact["partial_sequence"] = true
		}
		if rejected {
			compact["rejected_before_first_action"] = true
		}
	}
	rawActions, _ := full["actions"].([]map[string]any)
	if rawActions == nil {
		if generic, ok := full["actions"].([]any); ok {
			rawActions = make([]map[string]any, 0, len(generic))
			for _, item := range generic {
				if action, ok := item.(map[string]any); ok {
					rawActions = append(rawActions, action)
				}
			}
		}
	}
	actions := make([]map[string]any, 0, len(rawActions))
	for _, action := range rawActions {
		entry := map[string]any{
			"index": action["action_index"],
		}
		actionability, _ := action["actionability"].(map[string]any)
		if actionability != nil {
			if resolution := strings.TrimSpace(fmt.Sprint(
				actionability["resolution"],
			)); resolution != "" && resolution != "unique" &&
				resolution != "<nil>" {
				entry["resolution"] = resolution
			}
			for _, key := range []string{
				"delivery_verified",
				"postcondition_verified",
			} {
				value := compactTriState(actionability[key])
				if value == false {
					entry[key] = value
				}
			}
			for _, key := range []string{
				"stable",
				"window_focus_verified",
				"target_focus_verified",
				"point_owner_verified",
				"enabled",
				"editable_verified",
			} {
				if value := compactTriState(actionability[key]); value == false {
					entry[key] = false
				}
			}
		}
		if callError, _ := action["call_error"].(bool); callError {
			entry["type"] = action["action_type"]
			nonDefaultStringEvidence(
				entry, action, "failure_code", "", "none",
			)
			nonDefaultStringEvidence(
				entry, action, "failure_phase", "", "none",
			)
		}
		if postcondition, ok := action["postcondition"].(map[string]any); ok {
			status := strings.TrimSpace(fmt.Sprint(postcondition["status"]))
			reconciled, _ :=
				postcondition["reconciled_by_canonical_observation"].(bool)
			if status != "" && status != "<nil>" &&
				(status != "pass" || reconciled) {
				entry["type"] = action["action_type"]
				entry["postcondition"] = postcondition
			}
		}
		if len(entry) > 1 {
			actions = append(actions, entry)
		}
	}
	if len(actions) > 0 {
		compact["actions"] = actions
	}
	if postcondition, ok := full["postcondition"].(map[string]any); ok &&
		len(postcondition) > 0 {
		compact["postcondition"] = postcondition
	}
	return compact
}

func compactAXModelEvidence(
	full map[string]any,
	isError bool,
) map[string]any {
	compact := map[string]any{}
	mode := strings.TrimSpace(fmt.Sprint(full["ax_mode"]))
	coverageIncomplete := false
	if count, ok := intEvidence(full["coverage_incomplete_nodes"]); ok &&
		count > 0 {
		coverageIncomplete = true
	}

	treeComplete, hasTreeComplete := full["tree_complete_for_model"].(bool)
	if hasTreeComplete && !treeComplete && !coverageIncomplete {
		compact["tree_complete"] = false
	}
	structuralOmitted := false
	if count, ok := intEvidence(full["structural_budget_omitted_nodes"]); ok &&
		count > 0 {
		structuralOmitted = true
		compact["tree_complete"] = false
	}
	targetedCoverageFailure := coverageIncomplete &&
		axTargetedCoverageFailure(full, mode, isError)
	if coverageIncomplete &&
		(mode == a11yModeObserve || targetedCoverageFailure) {
		compact["coverage"] = "partial"
		compact["tree_complete"] = false
	}
	recover := map[string]any{}
	if structuralOmitted {
		recover["structural_budget"] = "find|root"
	}
	if targetedCoverageFailure {
		compact["target_resolution"] = "incomplete"
		if mode == a11yModeAct {
			recover["target"] = "find|GUI"
		} else {
			recover["target"] = "GUI"
		}
	}
	if len(recover) > 0 {
		compact["recover"] = recover
	}

	if batch := compactAXBatchResult(full["a11y_batch_result"]); len(batch) > 0 {
		compact["batch"] = batch
	}
	for _, key := range []string{
		"failure_code",
		"underlying_failure_code",
		"failure_phase",
		"failure_subject",
		"input_events_emitted",
		"partial_input_possible",
		"mutation_unknown",
		"effect_unknown",
		"observation_required",
		"observation_failure_code",
		"post_action_observation_status",
		"final_observation_returned",
		"next_identical_observation_required",
		"screenshot_returned",
		"target_window_absent",
		"close_effect_verified",
		"final_observation_kind",
		"source_window_status",
		"final_observation_window_ref",
		"final_observation_followed_window_transition",
		"window_identity_stabilized",
		"failure_facts_complete",
		"assertion_sampled_live_ui",
		"preflight_only",
		"predicate_verified",
		"postcondition_verified",
		"postcondition_recovered_by_final_observation",
		"postcondition_reconciled_by_canonical_observation",
		"replacement_window_ref",
		"recovery_observation_attempted",
		"recovery_observation_returned",
		"recovery_observation_failure_code",
		"action_outcome",
		"failed_action_input_emitted",
		"failed_action_caused_ui_change",
		"current_ui_returned",
		"current_state_cause",
		"recovery_read_attempted",
		"cause_unattributed",
		"recovery",
		"retry_same_request",
	} {
		if value, present := full[key]; present {
			if boolean, ok := value.(bool); ok && !boolean && !isError &&
				key != "input_events_emitted" &&
				key != "postcondition_verified" &&
				key != "final_observation_returned" &&
				key != "observation_required" &&
				key != "next_identical_observation_required" {
				continue
			}
			compact[key] = value
		}
	}
	if continuation, ok := full["continuation"].(map[string]any); ok {
		compactContinuation := map[string]any{}
		for _, key := range []string{
			"state",
			"retry_same_request",
			"route",
			"next_mode",
			"fresh_window_ref",
		} {
			if value, present := continuation[key]; present {
				compactContinuation[key] = value
			}
		}
		if len(compactContinuation) > 0 {
			compact["continuation"] = compactContinuation
		}
	}
	if isError {
		nonDefaultStringEvidence(
			compact, full, "evidence_scope", "", "error_only",
		)
	}
	return compact
}

func appendAXContinuationText(
	result toolCallResult,
	continuation map[string]any,
) toolCallResult {
	if len(continuation) == 0 {
		return result
	}
	parts := []string{}
	for _, key := range []string{
		"state",
		"retry_same_request",
		"route",
		"next_mode",
		"fresh_window_ref",
	} {
		if value, present := continuation[key]; present {
			parts = append(parts, fmt.Sprintf("%s=%v", key, value))
		}
	}
	if len(parts) == 0 {
		return result
	}
	line := "continuation: " + strings.Join(parts, " ")
	for _, item := range result.Content {
		if item.Type == "text" && strings.Contains(item.Text, line) {
			return result
		}
	}
	if len(result.Content) == 0 {
		result.Content = []contentItem{{Type: "text", Text: line}}
		return result
	}
	for index := len(result.Content) - 1; index >= 0; index-- {
		if result.Content[index].Type == "text" {
			text := strings.TrimSpace(result.Content[index].Text)
			if text == "" {
				result.Content[index].Text = line + "\n"
			} else {
				result.Content[index].Text = text + "\n" + line + "\n"
			}
			return result
		}
	}
	result.Content = append(result.Content, contentItem{Type: "text", Text: line + "\n"})
	return result
}

func delimitAXTextContent(result toolCallResult) toolCallResult {
	for index := range result.Content {
		if result.Content[index].Type != "text" {
			continue
		}
		if !strings.HasSuffix(result.Content[index].Text, "\n") {
			result.Content[index].Text += "\n"
		}
	}
	return result
}

func prepareAXEvidence(
	result toolCallResult,
) toolCallResult {
	result = delimitAXTextContent(result)
	result = normalizeAXEvidenceInvariants(result)
	if len(result.StructuredContent) == 0 {
		return result
	}
	full := result.StructuredContent
	if continuation, ok := full["continuation"].(map[string]any); ok {
		result = appendAXContinuationText(result, continuation)
	}
	result.StructuredContent = compactAXModelEvidence(full, result.IsError)
	return result
}

func normalizeAXEvidenceInvariants(
	result toolCallResult,
) toolCallResult {
	if len(result.StructuredContent) == 0 {
		return result
	}
	full := result.StructuredContent
	if axEvidenceReportsInputEmission(full) {
		full["input_events_emitted"] = true
		full["preflight_only"] = false
		if unknown, _ := full["mutation_unknown"].(bool); unknown {
			full["evidence_scope"] = "post_action_observation"
		}
	}
	return result
}

func axEvidenceReportsInputEmission(evidence map[string]any) bool {
	if evidence == nil {
		return false
	}
	if emitted, _ := evidence["input_events_emitted"].(bool); emitted {
		return true
	}
	switch runtimeEvidence := evidence["runtime_action_evidence"].(type) {
	case RuntimeActionEvidence:
		if runtimeEvidence.InputEmitted {
			return true
		}
	case *RuntimeActionEvidence:
		if runtimeEvidence != nil && runtimeEvidence.InputEmitted {
			return true
		}
	case map[string]any:
		if emitted, _ := runtimeEvidence["input_emitted"].(bool); emitted {
			return true
		}
	}
	switch actionability := evidence["actionability"].(type) {
	case ActionabilityEvidence:
		if actionability.InputEmitted == TriTrue {
			return true
		}
	case *ActionabilityEvidence:
		if actionability != nil && actionability.InputEmitted == TriTrue {
			return true
		}
	case map[string]any:
		if compactTriState(actionability["input_emitted"]) == true {
			return true
		}
	}
	batch, _ := evidence["a11y_batch_result"].(map[string]any)
	actions, _ := batch["actions"].([]map[string]any)
	for _, action := range actions {
		if axEvidenceReportsInputEmission(action) {
			return true
		}
	}
	if genericActions, ok := batch["actions"].([]any); ok {
		for _, item := range genericActions {
			action, _ := item.(map[string]any)
			if axEvidenceReportsInputEmission(action) {
				return true
			}
		}
	}
	if emitted, _ := evidence["input_emitted"].(bool); emitted {
		return true
	}
	return false
}

func persistAXTelemetry(
	args map[string]any,
	telemetry any,
) {
	path := strings.TrimSpace(
		os.Getenv("COMPUTER_USE_AX_TELEMETRY_LOG"),
	)
	if path == "" {
		path = "/tmp/computer-use-ax-telemetry.jsonl"
	}
	payload := map[string]any{
		"timestamp_utc": time.Now().UTC().Format(time.RFC3339Nano),
		"mode":          optionalString(args, "mode"),
		"scope":         optionalString(args, "scope"),
		"view":          optionalString(args, "view"),
		"telemetry":     telemetry,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AX telemetry marshal failed: %v\n", err)
		return
	}
	handle, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0600,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AX telemetry log open failed: %v\n", err)
		return
	}
	defer handle.Close()
	if _, err := handle.Write(append(encoded, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "AX telemetry log write failed: %v\n", err)
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
		response.Text = "No running top-level apps are visible to this Linux runtime."
	}
	return textResult(response.Text, false)
}

func formatZombieRuntimeDiagnostic(pid int) string {
	return fmt.Sprintf(
		"a11y_responsive=false cached_pid_present=zombie "+
			"window_for_cached_pid=false cached_pid=%d. The cached process "+
			"generation is defunct and cannot become ready; do not keep retrying "+
			"the same PID. Re-observe application identity before any relaunch.",
		pid)
}

func formatRuntimeTimeoutDiagnostic(
	pid int,
	hasProcessIdentity bool,
	processAlive bool,
	windowKnown bool,
	windowAlive bool,
) string {
	if !hasProcessIdentity {
		return "a11y_responsive=false cached_pid_present=unknown " +
			"window_for_cached_pid=unknown. " +
			"The accessibility bridge did not answer, and this MCP session has no " +
			"cached process identity for an independent liveness check."
	}
	if !processAlive {
		return fmt.Sprintf(
			"a11y_responsive=false cached_pid_present=false "+
				"window_for_cached_pid=false cached_pid=%d. The cached PID no longer "+
				"exists; the application may have exited or restarted.",
			pid)
	}
	if !windowKnown {
		return fmt.Sprintf(
			"a11y_responsive=false cached_pid_present=true "+
				"window_for_cached_pid=unknown cached_pid=%d. The cached PID exists "+
				"but the accessibility bridge did not answer; "+
				"the bounded X11 window probe was unavailable.",
			pid)
	}
	if windowAlive {
		return fmt.Sprintf(
			"a11y_responsive=false cached_pid_present=true "+
				"window_for_cached_pid=true cached_pid=%d. The cached PID and a managed "+
				"X11 window exist, but the accessibility "+
				"bridge did not answer. Inspect the GUI instead of retrying the same "+
				"A11Y call indefinitely.",
			pid)
	}
	return fmt.Sprintf(
		"a11y_responsive=false cached_pid_present=true "+
			"window_for_cached_pid=false cached_pid=%d. The cached PID exists without "+
			"a managed X11 window; the application may be rebuilding its UI. A bounded "+
			"retry can be appropriate.",
		pid)
}
