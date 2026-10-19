package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	// Path replaces Data for command-line and socket clients; MCP results
	// never set it.
	Path string `json:"path,omitempty"`
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

// outputMode reports whether large outputs are written to files ("file") or
// returned inline ("stdout"). In file mode, instead of compressing a large output, it writes the output to a
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
	"argument_error_code":                     true,
	"argument_error_path":                     true,
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

func init() {
	// Public AX error fields are rendered by withAXTextEvidence under their
	// public names; their internal spellings never reach the model.
	for _, key := range []string{
		axPublicErrorCodeKey, axPublicErrorPathKey,
		axPublicErrorHintKey, axPublicErrorDetailKey,
	} {
		modelPrivateEvidenceKeys[key] = true
	}
}

// withAXTextEvidence puts the AX evidence line first: one JSON object that
// starts with ok, then code, path, message, hint and counts.
func withAXTextEvidence(result toolCallResult) toolCallResult {
	if len(result.StructuredContent) == 0 {
		return result
	}
	visible := modelVisibleEvidence(result.StructuredContent)
	result.Content = append(
		[]contentItem{{Type: "text", Text: orderedJSON(visible, axPublicFields)}},
		result.Content...,
	)
	return result
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

// finalizeForModel is the last step before a result reaches a model, and
// every entry point that shows text to a model calls it, so that the MCP
// server and the command line return the same bytes.
func finalizeForModel(name string, result toolCallResult, structured bool) toolCallResult {
	return placeModelEvidence(name, prepareModelEvidence(name, result), structured)
}

// prepareModelEvidence applies the AX text budget and compacts the AX
// evidence into its public form. The budget runs first: its metadata
// (response size, truncation reason) must exist before the evidence line is
// serialized, or fields that live only in the structured channel disappear
// when that channel is removed.
func prepareModelEvidence(name string, result toolCallResult) toolCallResult {
	if name == publicToolAX {
		result = enforceAXResponseBudget(result)
		result = prepareAXEvidence(result)
	}
	return result
}

// placeModelEvidence keeps structuredContent for clients that negotiated it
// and opted in. Otherwise the evidence moves to the first text item and the
// structured field is dropped: some clients prefer structuredContent and
// hide sibling text from the model, which can make desktop state appear
// unavailable.
func placeModelEvidence(name string, result toolCallResult, structured bool) toolCallResult {
	if structured {
		return result
	}
	if name == publicToolAX {
		result = withAXTextEvidence(result)
	} else {
		result = withTextEvidence(result)
	}
	result.StructuredContent = nil
	return result
}

// callForModel runs one public tool call and returns the model-facing
// result. It is the MCP tools/call result and the computer-use call output.
func (s *service) callForModel(name string, args map[string]any, structured bool) toolCallResult {
	return finalizeForModel(name, s.callTool(name, args), structured)
}

// writeImagesToFiles replaces inline image data with files in the output
// directory. Command-line and socket clients get paths, never base64 on
// stdout.
func writeImagesToFiles(result toolCallResult) (toolCallResult, error) {
	content := make([]contentItem, 0, len(result.Content))
	for _, item := range result.Content {
		if item.Type != "image" || item.Data == "" {
			content = append(content, item)
			continue
		}
		path, err := writeImageFile(item.Data, item.MimeType)
		if err != nil {
			return result, err
		}
		content = append(content, contentItem{Type: "image", MimeType: item.MimeType, Path: path})
	}
	result.Content = content
	return result, nil
}

// writeImageFile decodes base64 image data into a new file in the output
// directory and returns its path.
func writeImageFile(data, mimeType string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	dir := outputDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	extension := ".png"
	if mimeType == "image/jpeg" {
		extension = ".jpg"
	}
	file, err := os.CreateTemp(dir, "screenshot-*"+extension)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return "", err
	}
	return file.Name(), file.Close()
}
