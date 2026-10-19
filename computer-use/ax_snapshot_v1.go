package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type CanonicalRole string

const (
	RoleGeneric CanonicalRole = "generic"
	RoleText    CanonicalRole = "text"
)

type TriState string

const (
	TriUnknown TriState = "unknown"
	TriFalse   TriState = "false"
	TriTrue    TriState = "true"
)

type CheckedState string

const (
	CheckedUnknown CheckedState = "unknown"
	CheckedFalse   CheckedState = "false"
	CheckedTrue    CheckedState = "true"
	CheckedMixed   CheckedState = "mixed"
)

type ActivationRoute string

const (
	ActivationNone         ActivationRoute = "none"
	ActivationSemantic     ActivationRoute = "semantic_action"
	ActivationLiveGeometry ActivationRoute = "live_geometry_fallback"
	ActivationUnknown      ActivationRoute = "unknown"
)

type AXStates struct {
	Active   TriState     `json:"active,omitempty"`
	Checked  CheckedState `json:"checked,omitempty"`
	Expanded TriState     `json:"expanded,omitempty"`
	Selected TriState     `json:"selected,omitempty"`
	Disabled TriState     `json:"disabled,omitempty"`
	Pressed  TriState     `json:"pressed,omitempty"`
	Invalid  TriState     `json:"invalid,omitempty"`
	Level    *int         `json:"level,omitempty"`
}

type AXCapabilities struct {
	Activate        ActivationRoute `json:"activate,omitempty"`
	SetValue        bool            `json:"setValue,omitempty"`
	EditText        bool            `json:"editText,omitempty"`
	Scroll          bool            `json:"scroll,omitempty"`
	Actions         []string        `json:"actions,omitempty"`
	ProviderActions []string        `json:"providerActions,omitempty"`
}

type AXProperties struct {
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Omitted     string `json:"omitted,omitempty"`
}

type AXProviderIdentity struct {
	BusName           string
	ObjectPath        string
	ProcessGeneration string
}

type AXNameProvenance struct {
	Source            string
	Contributors      []AXProviderIdentity
	RelationsComplete bool
	Confidence        string
}

type AXNode struct {
	ID                  string                        `json:"id"`
	Role                CanonicalRole                 `json:"role"`
	SemanticClass       string                        `json:"-"`
	ProviderRole        string                        `json:"providerRole,omitempty"`
	Name                string                        `json:"name,omitempty"`
	VisibilityEvidence  *AXVisibilityEvidence         `json:"visibilityEvidence,omitempty"`
	States              AXStates                      `json:"states,omitempty"`
	Capabilities        AXCapabilities                `json:"capabilities,omitempty"`
	Properties          AXProperties                  `json:"properties,omitempty"`
	Ref                 string                        `json:"ref,omitempty"`
	Children            []*AXNode                     `json:"children,omitempty"`
	Relations           map[string][]axRelationTarget `json:"relations,omitempty"`
	Annotations         []axProviderAnnotation        `json:"annotations,omitempty"`
	NameProvenance      AXNameProvenance              `json:"-"`
	SemanticFingerprint string                        `json:"semanticFingerprint"`
	ProviderIdentity    string                        `json:"-"`
	LocatorKind         AXLocatorKind                 `json:"-"`
	AutomationID        string                        `json:"-"`

	ProviderIndex                   int            `json:"-"`
	ProviderIndices                 []int          `json:"-"`
	RuntimeID                       []int          `json:"-"`
	RuntimeIDAliases                [][]int        `json:"-"`
	ProviderBusName                 string         `json:"-"`
	ProviderObjectPath              string         `json:"-"`
	ProcessGeneration               string         `json:"-"`
	ProviderIdentityScope           string         `json:"-"`
	ProviderWindowBusName           string         `json:"-"`
	ProviderWindowObjectPath        string         `json:"-"`
	ProviderWindowProcessGeneration string         `json:"-"`
	ProviderWindowRole              string         `json:"-"`
	ProviderWindowName              string         `json:"-"`
	Showing                         *bool          `json:"-"`
	Visible                         *bool          `json:"-"`
	SemanticTarget                  string         `json:"-"`
	SourceRecord                    *elementRecord `json:"-"`
	Parent                          *AXNode        `json:"-"`
	Synthetic                       bool           `json:"-"`
	ForceKeep                       bool           `json:"-"`
	FindMatch                       bool           `json:"-"`
	SemanticIdentityAmbiguous       bool           `json:"-"`
	AddressabilityEvaluated         bool           `json:"-"`
	AddressabilityReason            string         `json:"-"`
	BudgetOmittedNodes              int            `json:"-"`
	BudgetOmittedTargets            int            `json:"-"`
}

type DistillationStageReport struct {
	Name                 string `json:"name"`
	InputNodes           int    `json:"inputNodes"`
	OutputNodes          int    `json:"outputNodes"`
	RemovedNodes         int    `json:"removedNodes"`
	UnwrappedNodes       int    `json:"unwrappedNodes"`
	SuppressedProperties int    `json:"suppressedProperties"`
	BudgetApplied        bool   `json:"budgetApplied"`
	BudgetCharsBefore    int    `json:"budgetCharsBefore"`
	BudgetCharsAfter     int    `json:"budgetCharsAfter"`
	BudgetOmittedNodes   int    `json:"budgetOmittedNodes"`
	BudgetOmittedTargets int    `json:"budgetOmittedTargets"`
	BudgetExhausted      bool   `json:"budgetExhausted"`
	TargetCount          int    `json:"targetCount"`
	TargetRecall         int    `json:"targetRecall"`
	AmbiguityBefore      int    `json:"ambiguityBefore"`
	AmbiguityAfter       int    `json:"ambiguityAfter"`
	InvariantFailure     string `json:"invariantFailure,omitempty"`
}

type AXSnapshotV1 struct {
	SchemaVersion              string                    `json:"schemaVersion"`
	AppRef                     string                    `json:"appRef,omitempty"`
	AppIdentity                string                    `json:"appIdentity"`
	WindowHandle               string                    `json:"windowHandle"`
	WindowRef                  string                    `json:"-"`
	Revision                   string                    `json:"revision"`
	Roots                      []*AXNode                 `json:"roots"`
	Distillation               []DistillationStageReport `json:"distillation,omitempty"`
	AcquisitionIncomplete      bool                      `json:"-"`
	IdentityCoverageIncomplete bool                      `json:"-"`
	RefInventoryComplete       bool                      `json:"-"`
	// ActedRef is the rN the action that produced this snapshot targeted.
	// Projection keeps that node's full text.
	ActedRef string `json:"-"`

	CanonicalRoots []*AXNode          `json:"-"`
	ByID           map[string]*AXNode `json:"-"`
	ByIndex        map[int]*AXNode    `json:"-"`
}

type AXShadowStats struct {
	CanonicalNodes         int
	ProjectedNodes         int
	BaselineProjectedNodes int
	TargetRecords          int
	TargetRecall           int
	HierarchyMismatches    int
}

func runtimePathKey(path []int) string {
	if len(path) == 0 {
		return "/"
	}
	parts := make([]string, len(path))
	for index, value := range path {
		parts[index] = strconv.Itoa(value)
	}
	return strings.Join(parts, ".")
}

func recordInObservation(snapshot *appSnapshot, record *elementRecord) bool {
	if snapshot == nil || record == nil {
		return false
	}
	if snapshot.observationRoot != nil {
		return runtimePathContains(
			snapshot.observationRoot.RuntimeID,
			record.RuntimeID,
		)
	}
	return recordInA11yScope(snapshot, record)
}

func ontologyProviderRole(record *elementRecord) string {
	if record == nil {
		return ""
	}
	// ControlType is the provider's stable role vocabulary. LocalizedControlType
	// is display text and cannot be the ontology lookup key.
	if role := strings.TrimSpace(record.ControlType); role != "" {
		return strings.ToLower(role)
	}
	return strings.ToLower(strings.TrimSpace(record.LocalizedControlType))
}

type RoleDescriptor struct {
	ModelRole       CanonicalRole
	ProviderAliases []string
	SemanticClass   string
}

var roleOntology = []RoleDescriptor{
	{CanonicalRole("alert"), []string{"alert"}, "boundary"},
	{CanonicalRole("application"), []string{"application"}, "boundary"},
	{CanonicalRole("article"), []string{"article"}, "boundary"},
	{CanonicalRole("button"), []string{"push button", "toggle button", "button"}, "control"},
	{CanonicalRole("checkbox"), []string{"check box"}, "control"},
	{CanonicalRole("combobox"), []string{"combo box"}, "control"},
	{CanonicalRole("dialog"), []string{"dialog"}, "boundary"},
	{CanonicalRole("document"), []string{"document web", "document text", "document frame", "document"}, "boundary"},
	{CanonicalRole("grid"), []string{"document spreadsheet"}, "collection"},
	{CanonicalRole("table"), []string{"table"}, "collection"},
	{CanonicalRole("cell"), []string{"table cell"}, "item"},
	{CanonicalRole("heading"), []string{"heading"}, "content"},
	{CanonicalRole("img"), []string{"image", "icon"}, "content"},
	{CanonicalRole("link"), []string{"link"}, "control"},
	{CanonicalRole("list"), []string{"list", "list box"}, "collection"},
	{CanonicalRole("listitem"), []string{"list item"}, "item"},
	{CanonicalRole("menu"), []string{"menu"}, "boundary"},
	{CanonicalRole("menubar"), []string{"menu bar"}, "boundary"},
	{CanonicalRole("menuitem"), []string{"menu item"}, "control"},
	{CanonicalRole("menuitemcheckbox"), []string{"check menu item"}, "control"},
	{CanonicalRole("menuitemradio"), []string{"radio menu item"}, "control"},
	{CanonicalRole("option"), []string{"option"}, "item"},
	{CanonicalRole("paragraph"), []string{"paragraph"}, "content"},
	{CanonicalRole("progressbar"), []string{"progress bar", "busy indicator"}, "status"},
	{CanonicalRole("radio"), []string{"radio button"}, "control"},
	{CanonicalRole("scrollbar"), []string{"scroll bar"}, "control"},
	{CanonicalRole("separator"), []string{"separator"}, "structure"},
	{CanonicalRole("slider"), []string{"slider"}, "control"},
	{CanonicalRole("spinbutton"), []string{"spin button"}, "control"},
	{CanonicalRole("status"), []string{"status bar", "notification"}, "status"},
	{CanonicalRole("tab"), []string{"page tab"}, "control"},
	{CanonicalRole("tablist"), []string{"page tab list"}, "collection"},
	{CanonicalRole("textbox"), []string{"entry", "text area", "password text"}, "control"},
	{RoleText, []string{"static", "text", "label", "caption"}, "text"},
	{CanonicalRole("toolbar"), []string{"tool bar"}, "boundary"},
	{CanonicalRole("tree"), []string{"tree"}, "collection"},
	{CanonicalRole("treegrid"), []string{"tree table"}, "collection"},
	{CanonicalRole("treeitem"), []string{"tree item"}, "item"},
	{CanonicalRole("window"), []string{"frame", "window"}, "boundary"},
	{CanonicalRole("group"), []string{"panel", "root pane", "filler", "section", "group", "viewport", "scroll pane", "layered pane", "split pane"}, "structure"},
}

var roleByProvider = func() map[string]RoleDescriptor {
	out := map[string]RoleDescriptor{}
	for _, descriptor := range roleOntology {
		out[strings.ToLower(strings.TrimSpace(string(descriptor.ModelRole)))] = descriptor
		for _, alias := range descriptor.ProviderAliases {
			out[strings.ToLower(strings.TrimSpace(alias))] = descriptor
		}
	}
	return out
}()

func canonicalRoleDescriptor(providerRole string, parent *AXNode) RoleDescriptor {
	provider := strings.ToLower(strings.TrimSpace(providerRole))
	if provider == "table cell" && parent != nil &&
		(parent.Role == "grid" || parent.Role == "treegrid") {
		return RoleDescriptor{ModelRole: "gridcell", SemanticClass: "item"}
	}
	if descriptor, ok := roleByProvider[provider]; ok {
		return descriptor
	}
	return RoleDescriptor{ModelRole: RoleGeneric, SemanticClass: "unknown"}
}

func explicitState(tokens map[string]bool, positive, negative string) TriState {
	if tokens[positive] {
		return TriTrue
	}
	if negative != "" && tokens[negative] {
		return TriFalse
	}
	return TriUnknown
}

func typedAXStates(record *elementRecord) AXStates {
	tokens := map[string]bool{}
	for _, token := range recordStateTokens(record) {
		tokens[strings.ToLower(strings.TrimSpace(token))] = true
	}
	checked := CheckedUnknown
	switch {
	case tokens["indeterminate"] || tokens["mixed"]:
		checked = CheckedMixed
	case tokens["checked"]:
		checked = CheckedTrue
	case tokens["unchecked"]:
		checked = CheckedFalse
	}
	var level *int
	for token := range tokens {
		if strings.HasPrefix(token, "level=") {
			if parsed, err := strconv.Atoi(strings.TrimPrefix(token, "level=")); err == nil {
				level = &parsed
			}
		}
	}
	return AXStates{
		Active:   explicitState(tokens, "focused", ""),
		Checked:  checked,
		Expanded: explicitState(tokens, "expanded", "collapsed"),
		Selected: explicitState(tokens, "selected", "unselected"),
		Disabled: explicitState(tokens, "disabled", "enabled"),
		Pressed:  explicitState(tokens, "pressed", "not-pressed"),
		Invalid:  explicitState(tokens, "invalid", "valid"),
		Level:    level,
	}
}

func typedAXCapabilities(record *elementRecord, role CanonicalRole) AXCapabilities {
	providerActions := typedProviderActions(record)
	capabilities := AXCapabilities{
		Activate:        ActivationNone,
		ProviderActions: providerActions,
	}
	actionSet := map[string]bool{}
	for _, action := range providerActions {
		actionSet[normalizedActionName(action)] = true
	}
	if hasMeaningfulWorkingClick(record) || actionSet["click"] ||
		actionSet["activate"] || actionSet["press"] || actionSet["toggle"] {
		capabilities.Activate = ActivationSemantic
	}
	if record != nil && record.SetValueSupported != nil {
		capabilities.SetValue = *record.SetValueSupported
	} else {
		roleImpliesSetValue := role == "slider" || role == "spinbutton" || role == "combobox"
		if record != nil && strings.HasPrefix(record.ProviderBusName, "uia3:") {
			roleImpliesSetValue = false
		}
		capabilities.SetValue = actionSet["setvalue"] || roleImpliesSetValue
	}
	capabilities.EditText = role == "textbox"
	capabilities.Scroll = actionSet["scroll"] || role == "scrollbar"
	return capabilities
}

func typedProviderActions(record *elementRecord) []string {
	if record == nil {
		return nil
	}
	out := make([]string, 0, len(record.Actions))
	seen := map[string]bool{}
	for _, action := range record.Actions {
		normalized := normalizedActionName(action)
		if normalized == "" || normalized == "clickancestor" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, strings.ToLower(strings.TrimSpace(action)))
	}
	sort.Strings(out)
	return out
}

func appIdentityForSnapshot(snapshot *appSnapshot) string {
	if snapshot == nil {
		return ""
	}
	identity := strings.TrimSpace(snapshot.App.BundleIdentifier)
	if identity == "" {
		identity = strings.TrimSpace(snapshot.App.Name)
	}
	return strings.ToLower(identity)
}

func semanticFingerprint(
	window string,
	role CanonicalRole,
	name string,
	semanticTarget string,
	relations map[string][]axRelationTarget,
) string {
	parts := []string{
		strings.TrimSpace(window),
		string(role),
		comparableTreeText(name),
		strings.TrimSpace(semanticTarget),
	}
	relationParts := []string{}
	for kind, targets := range relations {
		for _, target := range targets {
			relationParts = append(relationParts,
				kind+":"+strings.ToLower(strings.TrimSpace(target.Role))+":"+
					comparableTreeText(target.Name))
		}
	}
	sort.Strings(relationParts)
	parts = append(parts, relationParts...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "f1:" + hex.EncodeToString(sum[:12])
}

func completeAXProviderIdentity(
	logicalWindowRef,
	processGeneration,
	providerBusName,
	providerObjectPath,
	providerIdentityScope,
	semanticTarget string,
) string {
	if strings.TrimSpace(logicalWindowRef) == "" ||
		strings.TrimSpace(processGeneration) == "" ||
		strings.TrimSpace(providerBusName) == "" ||
		strings.TrimSpace(providerObjectPath) == "" {
		return ""
	}
	parts := []string{
		strings.TrimSpace(logicalWindowRef),
		strings.TrimSpace(processGeneration),
		strings.TrimSpace(providerBusName),
		strings.TrimSpace(providerObjectPath),
	}
	if strings.TrimSpace(providerIdentityScope) != "" {
		parts = append(
			parts,
			strings.TrimSpace(providerIdentityScope),
			strings.TrimSpace(semanticTarget),
		)
	}
	return strings.Join(parts, "\x00")
}

func semanticDiagnosticSelector(
	role CanonicalRole,
	record *elementRecord,
) string {
	if record == nil {
		return ""
	}
	name := normalizedTreeText(record.Name)
	if name != "" {
		return selectorHead(string(role), name)
	}
	if description := normalizedTreeText(record.Description); description != "" {
		return fmt.Sprintf(
			"%s [desc=%s]",
			strings.TrimSpace(string(role)),
			strconv.Quote(description),
		)
	}
	if placeholder := normalizedTreeText(record.Placeholder); placeholder != "" {
		return fmt.Sprintf(
			"%s [placeholder=%s]",
			strings.TrimSpace(string(role)),
			strconv.Quote(placeholder),
		)
	}
	return ""
}

func mergeTriState(first, second TriState) TriState {
	if first == second {
		return first
	}
	return TriUnknown
}

func mergeCheckedState(first, second CheckedState) CheckedState {
	if first == second {
		return first
	}
	return CheckedUnknown
}

func mergeBoolEvidence(first, second *bool) *bool {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	if *first != *second {
		return nil
	}
	return first
}

type axProviderIdentityConflict struct {
	reason string
}

func (err *axProviderIdentityConflict) Error() string {
	return "provider_identity_conflict: contradictory " + err.reason + " in accessibility records"
}

func conflictingNonemptyIdentity(first, second string) bool {
	first, second = strings.TrimSpace(first), strings.TrimSpace(second)
	return first != "" && second != "" && first != second
}

func validateExactProviderAlias(node *AXNode, record *elementRecord, role CanonicalRole) error {
	if node.Role != role {
		return &axProviderIdentityConflict{reason: "roles"}
	}
	if conflictingNonemptyIdentity(node.AutomationID, record.AutomationID) {
		return &axProviderIdentityConflict{reason: "control identifiers"}
	}
	for _, pair := range [][2]string{
		{node.ProviderWindowBusName, record.ProviderWindowBusName},
		{node.ProviderWindowObjectPath, record.ProviderWindowObjectPath},
		{node.ProviderWindowProcessGeneration, record.ProviderWindowProcessGeneration},
	} {
		if conflictingNonemptyIdentity(pair[0], pair[1]) {
			return &axProviderIdentityConflict{reason: "window ownership"}
		}
	}
	return nil
}

func retainProviderAliasIdentity(node *AXNode, record *elementRecord) {
	node.AutomationID = firstNonEmpty(node.AutomationID, record.AutomationID)
	node.ProviderWindowBusName = firstNonEmpty(node.ProviderWindowBusName, record.ProviderWindowBusName)
	node.ProviderWindowObjectPath = firstNonEmpty(node.ProviderWindowObjectPath, record.ProviderWindowObjectPath)
	node.ProviderWindowProcessGeneration = firstNonEmpty(
		node.ProviderWindowProcessGeneration, record.ProviderWindowProcessGeneration,
	)
}

func mergeExactProviderAlias(node *AXNode, record *elementRecord) {
	if node == nil || record == nil {
		return
	}
	node.ProviderIndices = append(node.ProviderIndices, record.Index)
	node.RuntimeIDAliases = append(
		node.RuntimeIDAliases,
		append([]int(nil), record.RuntimeID...),
	)
	aliasStates := typedAXStates(record)
	node.States.Active = mergeTriState(node.States.Active, aliasStates.Active)
	node.States.Checked = mergeCheckedState(node.States.Checked, aliasStates.Checked)
	node.States.Expanded = mergeTriState(node.States.Expanded, aliasStates.Expanded)
	node.States.Selected = mergeTriState(node.States.Selected, aliasStates.Selected)
	node.States.Disabled = mergeTriState(node.States.Disabled, aliasStates.Disabled)
	node.States.Pressed = mergeTriState(node.States.Pressed, aliasStates.Pressed)
	node.States.Invalid = mergeTriState(node.States.Invalid, aliasStates.Invalid)
	if node.States.Level != nil && aliasStates.Level != nil &&
		*node.States.Level != *aliasStates.Level {
		node.States.Level = nil
	} else if node.States.Level == nil {
		node.States.Level = aliasStates.Level
	}
	aliasCapabilities := typedAXCapabilities(record, node.Role)
	if node.Capabilities.Activate == ActivationNone {
		node.Capabilities.Activate = aliasCapabilities.Activate
	} else if aliasCapabilities.Activate != ActivationNone &&
		node.Capabilities.Activate != aliasCapabilities.Activate {
		node.Capabilities.Activate = ActivationUnknown
	}
	node.Capabilities.SetValue = node.Capabilities.SetValue || aliasCapabilities.SetValue
	node.Capabilities.EditText = node.Capabilities.EditText || aliasCapabilities.EditText
	node.Capabilities.Scroll = node.Capabilities.Scroll || aliasCapabilities.Scroll
	node.Showing = mergeBoolEvidence(node.Showing, record.Showing)
	node.Visible = mergeBoolEvidence(node.Visible, record.Visible)
	node.VisibilityEvidence = mergeVisibilityEvidence(node.VisibilityEvidence, record.VisibilityEvidence)
	if node.VisibilityEvidence != nil && node.VisibilityEvidence.State == "unknown" {
		node.Showing, node.Visible = nil, nil
	}
	// Keep later owner evidence so a third alias cannot hide a conflict.
	retainProviderAliasIdentity(node, record)
}

func buildAXSnapshotV1(snapshot *appSnapshot) (*AXSnapshotV1, error) {
	if snapshot == nil {
		return nil, errors.New("cannot build AXSnapshotV1 from nil snapshot")
	}
	coverage := providerCoverageSummary(snapshot.Annotations)
	identityCoverageIncomplete :=
		coverage.AcquisitionIncomplete > 0 ||
			coverage.ExpandableHidden > 0
	out := &AXSnapshotV1{
		SchemaVersion:              "AXSnapshotV1",
		AppRef:                     snapshot.publicAppRef,
		AppIdentity:                appIdentityForSnapshot(snapshot),
		WindowHandle:               snapshot.WindowHandle,
		WindowRef:                  snapshot.publicWindowRef,
		Revision:                   snapshot.SnapshotID,
		AcquisitionIncomplete:      coverage.AcquisitionIncomplete > 0,
		IdentityCoverageIncomplete: identityCoverageIncomplete,
		RefInventoryComplete: !identityCoverageIncomplete &&
			snapshot.observationRoot == nil &&
			normalizedA11yScope(snapshot.scope) == a11yScopeWindow &&
			isShortAXWindowRef(snapshot.publicWindowRef),
		ByID:    map[string]*AXNode{},
		ByIndex: map[int]*AXNode{},
	}
	byProviderIdentity := map[string]*AXNode{}
	processIdentityEvidence := map[string]*AXNode{}
	for index := range snapshot.Elements {
		record := &snapshot.Elements[index]
		if !recordInObservation(snapshot, record) {
			continue
		}
		id := runtimePathKey(record.RuntimeID)
		if _, exists := out.ByID[id]; exists {
			return nil, fmt.Errorf("duplicate AXSnapshotV1 node id %q", id)
		}
		if _, exists := out.ByIndex[record.Index]; exists {
			return nil, fmt.Errorf("duplicate AXSnapshotV1 provider index %d", record.Index)
		}
		var parent *AXNode
		for depth := len(record.RuntimeID) - 1; depth >= 0; depth-- {
			if candidate := out.ByID[runtimePathKey(record.RuntimeID[:depth])]; candidate != nil {
				parent = candidate
				break
			}
		}
		providerRole := ontologyProviderRole(record)
		descriptor := canonicalRoleDescriptor(providerRole, parent)
		semanticTarget := semanticRefForRecord(snapshot, *record)
		providerIdentity := completeAXProviderIdentity(
			snapshot.publicWindowRef,
			record.ProcessGeneration,
			record.ProviderBusName,
			record.ProviderObjectPath,
			record.ProviderIdentityScope,
			semanticTarget,
		)
		// Process object identity is independent of its projected label.
		identityScope := strings.TrimSpace(record.ProviderIdentityScope)
		if identityScope == "process" || identityScope == "process_instance" {
			objectKey := completeAXProviderIdentity(
				snapshot.publicWindowRef, record.ProcessGeneration,
				record.ProviderBusName, record.ProviderObjectPath, identityScope, "",
			)
			if objectKey != "" {
				identity := processIdentityEvidence[objectKey]
				if identity == nil {
					identity = &AXNode{Role: descriptor.ModelRole}
					processIdentityEvidence[objectKey] = identity
				} else if err := validateExactProviderAlias(identity, record, descriptor.ModelRole); err != nil {
					return nil, err
				}
				retainProviderAliasIdentity(identity, record)
			}
		}
		if providerIdentity != "" {
			if canonical := byProviderIdentity[providerIdentity]; canonical != nil {
				if err := validateExactProviderAlias(canonical, record, descriptor.ModelRole); err != nil {
					return nil, err
				}
				out.ByID[id] = canonical
				out.ByIndex[record.Index] = canonical
				mergeExactProviderAlias(canonical, record)
				continue
			}
		}
		locatorKind := AXLocatorKind("")
		if semanticTarget != "" {
			locatorKind = AXLocatorSemantic
			if (descriptor.ModelRole == "cell" ||
				descriptor.ModelRole == "gridcell") &&
				spreadsheetRangeValue.MatchString(
					normalizedTreeText(record.Name),
				) {
				// An A1-looking name is not a complete spreadsheet identity.
				// Until document/sheet/table ownership is available, bind the
				// ref to this exact provider object instead of allowing C2 in a
				// different sheet to recover the same public ref.
				if providerIdentity != "" {
					locatorKind = AXLocatorProviderBound
				} else {
					semanticTarget = ""
					locatorKind = ""
				}
			}
		} else if providerIdentity != "" {
			semanticTarget = semanticDiagnosticSelector(
				descriptor.ModelRole,
				record,
			)
			if semanticTarget != "" {
				locatorKind = AXLocatorProviderBound
			}
		}
		node := &AXNode{
			ID:            id,
			Role:          descriptor.ModelRole,
			SemanticClass: descriptor.SemanticClass,
			ProviderRole:  providerRole,
			Name:          normalizedTreeText(record.Name),
			States:        typedAXStates(record),
			Properties: AXProperties{
				Value:       normalizedTreeText(record.Value),
				Description: normalizedTreeText(record.Description),
				Placeholder: normalizedTreeText(record.Placeholder),
			},
			Relations: record.Relations,
			NameProvenance: AXNameProvenance{
				Source:            strings.TrimSpace(record.NameSource),
				Contributors:      nameContributorIdentities(record),
				RelationsComplete: record.RelationReadComplete,
				Confidence:        nameProvenanceConfidence(record),
			},
			ProviderIndex:   record.Index,
			ProviderIndices: []int{record.Index},
			RuntimeID:       append([]int(nil), record.RuntimeID...),
			RuntimeIDAliases: [][]int{
				append([]int(nil), record.RuntimeID...),
			},
			ProviderBusName:                 record.ProviderBusName,
			ProviderObjectPath:              record.ProviderObjectPath,
			ProcessGeneration:               record.ProcessGeneration,
			ProviderIdentityScope:           record.ProviderIdentityScope,
			ProviderWindowBusName:           record.ProviderWindowBusName,
			ProviderWindowObjectPath:        record.ProviderWindowObjectPath,
			ProviderWindowProcessGeneration: record.ProviderWindowProcessGeneration,
			ProviderWindowRole:              record.ProviderWindowRole,
			ProviderWindowName:              record.ProviderWindowName,
			ProviderIdentity:                providerIdentity,
			LocatorKind:                     locatorKind,
			AutomationID:                    strings.TrimSpace(record.AutomationID),
			Showing:                         record.Showing,
			Visible:                         record.Visible,
			VisibilityEvidence:              cloneVisibilityEvidence(record.VisibilityEvidence),
			SemanticTarget:                  semanticTarget,
			SourceRecord:                    record,
			Parent:                          parent,
		}
		if node.VisibilityEvidence != nil && node.VisibilityEvidence.State == "unknown" {
			node.Showing, node.Visible = nil, nil
		}
		node.Capabilities = typedAXCapabilities(record, node.Role)
		node.SemanticFingerprint = semanticFingerprint(
			snapshot.WindowHandle,
			node.Role,
			node.Name,
			semanticTarget,
			node.Relations,
		)
		out.ByID[id] = node
		out.ByIndex[record.Index] = node
		if providerIdentity != "" {
			byProviderIdentity[providerIdentity] = node
		}
		if parent == nil {
			out.CanonicalRoots = append(out.CanonicalRoots, node)
		} else {
			parent.Children = append(parent.Children, node)
		}
	}
	for _, annotation := range snapshot.Annotations {
		if parent := out.ByID[runtimePathKey(annotation.ParentRuntimeID)]; parent != nil {
			parent.Annotations = append(parent.Annotations, annotation)
		}
	}
	out.Roots = cloneAXForest(out.CanonicalRoots, nil)
	return out, out.Validate()
}

func nameContributorIdentities(record *elementRecord) []AXProviderIdentity {
	if record == nil {
		return nil
	}
	targets := record.Relations["labelled-by"]
	identities := make([]AXProviderIdentity, 0, len(targets))
	seen := map[string]bool{}
	for _, target := range targets {
		key := providerObjectIdentityKey(
			target.ProviderBusName,
			target.ProviderObjectPath,
			target.ProcessGeneration,
		)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		identities = append(identities, AXProviderIdentity{
			BusName:           strings.TrimSpace(target.ProviderBusName),
			ObjectPath:        strings.TrimSpace(target.ProviderObjectPath),
			ProcessGeneration: strings.TrimSpace(target.ProcessGeneration),
		})
	}
	return identities
}

func nameProvenanceConfidence(record *elementRecord) string {
	if record == nil {
		return "unknown"
	}
	switch strings.TrimSpace(record.NameSource) {
	case "provider":
		return "provider_declared"
	case "labelled_by":
		if record.RelationReadComplete &&
			len(nameContributorIdentities(record)) > 0 {
			return "exact"
		}
		return "inferred"
	case "synthetic_text_run":
		return "inferred"
	default:
		return "unknown"
	}
}

func cloneAXForest(nodes []*AXNode, parent *AXNode) []*AXNode {
	out := make([]*AXNode, 0, len(nodes))
	for _, node := range nodes {
		clone := *node
		clone.VisibilityEvidence = cloneVisibilityEvidence(node.VisibilityEvidence)
		clone.Parent = parent
		clone.Children = cloneAXForest(node.Children, &clone)
		clone.RuntimeID = append([]int(nil), node.RuntimeID...)
		clone.ProviderIndices = append([]int(nil), node.ProviderIndices...)
		clone.RuntimeIDAliases = make([][]int, 0, len(node.RuntimeIDAliases))
		for _, alias := range node.RuntimeIDAliases {
			clone.RuntimeIDAliases = append(
				clone.RuntimeIDAliases,
				append([]int(nil), alias...),
			)
		}
		clone.Capabilities.Actions = append([]string(nil), node.Capabilities.Actions...)
		clone.Capabilities.ProviderActions = append(
			[]string(nil), node.Capabilities.ProviderActions...,
		)
		clone.Annotations = append([]axProviderAnnotation(nil), node.Annotations...)
		out = append(out, &clone)
	}
	return out
}

func (snapshot *AXSnapshotV1) Validate() error {
	if snapshot == nil {
		return errors.New("nil AXSnapshotV1")
	}
	if snapshot.SchemaVersion != "AXSnapshotV1" {
		return fmt.Errorf("unexpected AX schema version %q", snapshot.SchemaVersion)
	}
	seen := map[string]bool{}
	var visit func([]*AXNode, *AXNode) error
	visit = func(nodes []*AXNode, parent *AXNode) error {
		for _, node := range nodes {
			if node == nil || node.ID == "" || node.Role == "" {
				return errors.New("AXSnapshotV1 contains an incomplete node")
			}
			if seen[node.ID] && !node.Synthetic {
				return fmt.Errorf("duplicate AXSnapshotV1 node id %q", node.ID)
			}
			seen[node.ID] = true
			if node.Parent != parent {
				return fmt.Errorf("AXSnapshotV1 parent mismatch at %q", node.ID)
			}
			if err := visit(node.Children, node); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(snapshot.Roots, nil)
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
