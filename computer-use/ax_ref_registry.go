package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const defaultAXRefRegistryLimit = 4096
const defaultAXWindowRefRegistryLimit = 256
const defaultAXAppRefRegistryLimit = 128

type AXLocatorKind string

const (
	AXLocatorSemantic      AXLocatorKind = "semantic"
	AXLocatorProviderBound AXLocatorKind = "provider_bound"
)

type AXRefLifecycle string

const (
	AXRefActive            AXRefLifecycle = "active"
	AXRefNeedsRevalidation AXRefLifecycle = "needs_revalidation"
	AXRefStale             AXRefLifecycle = "stale"
	AXRefExpired           AXRefLifecycle = "expired"
	AXRefSuperseded        AXRefLifecycle = "superseded"
)

type AXAppRefEntry struct {
	Ref               string
	IdentityKey       string
	DisplayName       string
	ProviderBusName   string
	ProcessGeneration string
	CreatedRevision   string
	LastSeenRevision  string
	LastUsedTick      uint64
	Lifecycle         AXRefLifecycle
	LifecycleReason   string
}

type AppRefRegistry struct {
	next       uint64
	tick       uint64
	limit      int
	entries    map[string]*AXAppRefEntry
	byIdentity map[string]string
}

func (registry *AppRefRegistry) clone() *AppRefRegistry {
	if registry == nil {
		return nil
	}
	clone := &AppRefRegistry{
		next:       registry.next,
		tick:       registry.tick,
		limit:      registry.limit,
		entries:    make(map[string]*AXAppRefEntry, len(registry.entries)),
		byIdentity: make(map[string]string, len(registry.byIdentity)),
	}
	for ref, entry := range registry.entries {
		if entry == nil {
			clone.entries[ref] = nil
			continue
		}
		entryCopy := *entry
		clone.entries[ref] = &entryCopy
	}
	for identity, ref := range registry.byIdentity {
		clone.byIdentity[identity] = ref
	}
	return clone
}

type AXRefEntry struct {
	Ref                             string
	IdentityKey                     string
	LocatorKind                     AXLocatorKind
	Lifecycle                       AXRefLifecycle
	StaleReason                     string
	AppRef                          string
	AppIdentity                     string
	LogicalWindowRef                string
	WindowHandle                    string
	ProviderBusName                 string
	ProviderObjectPath              string
	ProcessGeneration               string
	ProviderIdentityScope           string
	ProviderWindowBusName           string
	ProviderWindowObjectPath        string
	ProviderWindowProcessGeneration string
	ProviderWindowRole              string
	ProviderWindowName              string
	RuntimeID                       []int
	RuntimeIDAliases                [][]int
	SemanticTarget                  string
	Role                            CanonicalRole
	Name                            string
	CreatedRevision                 string
	LastSeenRevision                string
	LastUsedTick                    uint64
}

func copyRuntimeIDs(paths [][]int) [][]int {
	out := make([][]int, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		key := fmt.Sprint(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, append([]int(nil), path...))
	}
	return out
}

func (registry *RefRegistry) BindRuntimePaths(
	ref string,
	runtimeID []int,
	aliases [][]int,
) {
	if registry == nil {
		return
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		return
	}
	entry.RuntimeID = append([]int(nil), runtimeID...)
	entry.RuntimeIDAliases = copyRuntimeIDs(aliases)
}

func (registry *RefRegistry) RefreshProviderBinding(
	ref string,
	record *elementRecord,
	allowProviderRebind bool,
) bool {
	if registry == nil || record == nil {
		return false
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil || entry.Lifecycle != AXRefActive ||
		entry.LocatorKind != AXLocatorSemantic {
		return false
	}
	if !runtimePathMatchesEntry(entry, record.RuntimeID) {
		return false
	}
	if completeProviderIdentityChanged(
		entry,
		record.ProviderBusName,
		record.ProviderObjectPath,
		record.ProcessGeneration,
	) && !allowProviderRebind {
		return false
	}
	role := canonicalRoleDescriptor(record.ControlType, nil).ModelRole
	if !providerSemanticContinuity(
		entry,
		role,
		record.Name,
		entry.SemanticTarget,
	) {
		return false
	}
	entry.ProviderBusName = strings.TrimSpace(record.ProviderBusName)
	entry.ProviderObjectPath = strings.TrimSpace(record.ProviderObjectPath)
	entry.ProcessGeneration = strings.TrimSpace(record.ProcessGeneration)
	entry.ProviderIdentityScope =
		strings.TrimSpace(record.ProviderIdentityScope)
	entry.ProviderWindowBusName =
		strings.TrimSpace(record.ProviderWindowBusName)
	entry.ProviderWindowObjectPath =
		strings.TrimSpace(record.ProviderWindowObjectPath)
	entry.ProviderWindowProcessGeneration =
		strings.TrimSpace(record.ProviderWindowProcessGeneration)
	entry.ProviderWindowRole = normalizedTreeText(record.ProviderWindowRole)
	entry.ProviderWindowName = normalizedTreeText(record.ProviderWindowName)
	entry.RuntimeID = append([]int(nil), record.RuntimeID...)
	entry.RuntimeIDAliases = copyRuntimeIDs([][]int{record.RuntimeID})
	entry.StaleReason = ""
	return true
}

type RefRegistry struct {
	next       uint64
	tick       uint64
	limit      int
	entries    map[string]*AXRefEntry
	byIdentity map[string]string
}

func (registry *RefRegistry) clone() *RefRegistry {
	if registry == nil {
		return nil
	}
	clone := &RefRegistry{
		next:       registry.next,
		tick:       registry.tick,
		limit:      registry.limit,
		entries:    make(map[string]*AXRefEntry, len(registry.entries)),
		byIdentity: make(map[string]string, len(registry.byIdentity)),
	}
	for ref, entry := range registry.entries {
		if entry == nil {
			clone.entries[ref] = nil
			continue
		}
		entryCopy := *entry
		entryCopy.RuntimeID = append([]int(nil), entry.RuntimeID...)
		entryCopy.RuntimeIDAliases = copyRuntimeIDs(entry.RuntimeIDAliases)
		clone.entries[ref] = &entryCopy
	}
	for identity, ref := range registry.byIdentity {
		clone.byIdentity[identity] = ref
	}
	return clone
}

type AXWindowRefEntry struct {
	Ref                             string
	IdentityKey                     string
	AppRef                          string
	AppIdentity                     string
	WindowHandle                    string
	SemanticTarget                  string
	ProviderWindowBusName           string
	ProviderWindowObjectPath        string
	ProviderWindowProcessGeneration string
	ProviderWindowRole              string
	ProviderWindowName              string
	CreatedRevision                 string
	LastSeenRevision                string
	LastUsedTick                    uint64
	Lifecycle                       AXRefLifecycle
	LifecycleReason                 string
}

func lifecycleResolveError(kind, ref string, lifecycle AXRefLifecycle, reason, recovery string) error {
	if lifecycle == "" || lifecycle == AXRefActive {
		return nil
	}
	if reason == "" {
		reason = string(lifecycle)
	}
	return fmt.Errorf(
		"short AX %s ref %q is %s (%s); %s",
		kind, ref, lifecycle, reason, recovery,
	)
}

func NewAppRefRegistry(limit int) *AppRefRegistry {
	if limit <= 0 {
		limit = defaultAXAppRefRegistryLimit
	}
	return &AppRefRegistry{
		next:       1,
		limit:      limit,
		entries:    map[string]*AXAppRefEntry{},
		byIdentity: map[string]string{},
	}
}

func appRefIdentityKey(providerBusName, processGeneration string) string {
	return strings.Join([]string{
		strings.TrimSpace(providerBusName),
		strings.TrimSpace(processGeneration),
	}, "\x00")
}

func (registry *AppRefRegistry) Acquire(
	displayName,
	providerBusName,
	processGeneration,
	revision string,
) string {
	if registry == nil {
		return ""
	}
	key := appRefIdentityKey(providerBusName, processGeneration)
	if strings.Trim(key, "\x00") == "" ||
		strings.TrimSpace(providerBusName) == "" ||
		strings.TrimSpace(processGeneration) == "" {
		return ""
	}
	registry.tick++
	if ref := registry.byIdentity[key]; ref != "" {
		if entry := registry.entries[ref]; entry != nil {
			if entry.Lifecycle != AXRefActive &&
				entry.Lifecycle != AXRefNeedsRevalidation {
				delete(registry.byIdentity, key)
			} else {
				entry.DisplayName = strings.TrimSpace(displayName)
				entry.LastSeenRevision = revision
				entry.LastUsedTick = registry.tick
				entry.Lifecycle = AXRefActive
				entry.LifecycleReason = ""
				return ref
			}
		}
		delete(registry.byIdentity, key)
	}
	ref := "a" + strconv.FormatUint(registry.next, 10)
	registry.next++
	registry.entries[ref] = &AXAppRefEntry{
		Ref:               ref,
		IdentityKey:       key,
		DisplayName:       strings.TrimSpace(displayName),
		ProviderBusName:   strings.TrimSpace(providerBusName),
		ProcessGeneration: strings.TrimSpace(processGeneration),
		CreatedRevision:   revision,
		LastSeenRevision:  revision,
		LastUsedTick:      registry.tick,
		Lifecycle:         AXRefActive,
	}
	registry.byIdentity[key] = ref
	registry.evict()
	return ref
}

func (registry *AppRefRegistry) evict() {
	for registry != nil && len(registry.entries) > registry.limit {
		oldestRef := ""
		var oldestTick uint64
		for ref, entry := range registry.entries {
			if oldestRef == "" || entry.LastUsedTick < oldestTick {
				oldestRef = ref
				oldestTick = entry.LastUsedTick
			}
		}
		entry := registry.entries[oldestRef]
		delete(registry.entries, oldestRef)
		if entry != nil {
			delete(registry.byIdentity, entry.IdentityKey)
		}
	}
}

func parseMonotonicAXAppRef(ref string) (uint64, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 2 || ref[0] != 'a' {
		return 0, false
	}
	value, err := strconv.ParseUint(ref[1:], 10, 64)
	return value, err == nil && value > 0
}

func (registry *AppRefRegistry) Resolve(ref string) (*AXAppRefEntry, error) {
	id, ok := parseMonotonicAXAppRef(ref)
	if !ok {
		return nil, fmt.Errorf("invalid short AX app ref %q", ref)
	}
	if registry == nil {
		return nil, errors.New("AX app ref registry is unavailable")
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		if id < registry.next {
			return nil, fmt.Errorf(
				"short AX app ref %q expired from this session registry; observe applications again",
				ref,
			)
		}
		return nil, fmt.Errorf(
			"short AX app ref %q was never issued in this session",
			ref,
		)
	}
	if err := lifecycleResolveError(
		"app", ref, entry.Lifecycle, entry.LifecycleReason,
		"observe applications again",
	); err != nil {
		return nil, err
	}
	registry.tick++
	entry.LastUsedTick = registry.tick
	copy := *entry
	return &copy, nil
}

func (registry *AppRefRegistry) MarkLifecycle(
	ref string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if registry == nil {
		return
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		return
	}
	entry.Lifecycle = lifecycle
	entry.LifecycleReason = strings.TrimSpace(reason)
	if lifecycle != AXRefActive && lifecycle != AXRefNeedsRevalidation {
		delete(registry.byIdentity, entry.IdentityKey)
	}
}

type WindowRefRegistry struct {
	next       uint64
	tick       uint64
	limit      int
	entries    map[string]*AXWindowRefEntry
	byIdentity map[string]string
}

func (registry *WindowRefRegistry) clone() *WindowRefRegistry {
	if registry == nil {
		return nil
	}
	clone := &WindowRefRegistry{
		next:       registry.next,
		tick:       registry.tick,
		limit:      registry.limit,
		entries:    make(map[string]*AXWindowRefEntry, len(registry.entries)),
		byIdentity: make(map[string]string, len(registry.byIdentity)),
	}
	for ref, entry := range registry.entries {
		if entry == nil {
			clone.entries[ref] = nil
			continue
		}
		entryCopy := *entry
		clone.entries[ref] = &entryCopy
	}
	for identity, ref := range registry.byIdentity {
		clone.byIdentity[identity] = ref
	}
	return clone
}

func NewRefRegistry(limit int) *RefRegistry {
	if limit <= 0 {
		limit = defaultAXRefRegistryLimit
	}
	return &RefRegistry{
		next:       1,
		limit:      limit,
		entries:    map[string]*AXRefEntry{},
		byIdentity: map[string]string{},
	}
}

func NewWindowRefRegistry(limit int) *WindowRefRegistry {
	if limit <= 0 {
		limit = defaultAXWindowRefRegistryLimit
	}
	return &WindowRefRegistry{
		next:       1,
		limit:      limit,
		entries:    map[string]*AXWindowRefEntry{},
		byIdentity: map[string]string{},
	}
}

func windowRefIdentityKey(appIdentity, windowHandle string) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(appIdentity)),
		strings.TrimSpace(windowHandle),
	}, "\x00")
}

func modalWindowRole(role string) bool {
	switch normalizedTreeText(role) {
	case "alert", "alert dialog", "dialog", "file chooser", "color chooser":
		return true
	default:
		return false
	}
}

func windowProviderBindingContinuous(
	entry *AXWindowRefEntry,
	providerBusName,
	providerObjectPath,
	processGeneration,
	role,
	_ string,
) bool {
	if entry == nil {
		return false
	}
	wasModal := modalWindowRole(entry.ProviderWindowRole)
	isModal := modalWindowRole(role)
	if wasModal != isModal {
		return false
	}
	if !isModal {
		// A real top-level native window may rebuild its AX object or change its
		// title while retaining the same native handle and process generation.
		return true
	}
	// Toolkits commonly host transient dialogs inside their owner's native
	// window. In that case the native handle alone cannot distinguish a later
	// dialog instance from the original one, so preserve a modal ref only while
	// its provider owner and role family remain continuous. The accessible name
	// is mutable UI state: settings pages may put the active section in the
	// title without creating a new logical window.
	return strings.TrimSpace(entry.ProviderWindowBusName) ==
		strings.TrimSpace(providerBusName) &&
		strings.TrimSpace(entry.ProviderWindowObjectPath) ==
			strings.TrimSpace(providerObjectPath) &&
		strings.TrimSpace(entry.ProviderWindowProcessGeneration) ==
			strings.TrimSpace(processGeneration) &&
		normalizedTreeText(entry.ProviderWindowRole) ==
			normalizedTreeText(role)
}

func validInternalWindowHandle(windowHandle string) bool {
	const prefix = "w2:"
	windowHandle = strings.TrimSpace(windowHandle)
	if !strings.HasPrefix(windowHandle, prefix) ||
		len(windowHandle) != len(prefix)+32 {
		return false
	}
	for _, char := range windowHandle[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (registry *WindowRefRegistry) Acquire(
	appIdentity,
	windowHandle,
	semanticTarget,
	revision string,
	providerIdentity ...string,
) string {
	appIdentity = strings.ToLower(strings.TrimSpace(appIdentity))
	windowHandle = strings.TrimSpace(windowHandle)
	semanticTarget = strings.TrimSpace(semanticTarget)
	if registry == nil || appIdentity == "" ||
		!validInternalWindowHandle(windowHandle) ||
		semanticTarget == "" {
		return ""
	}
	registry.tick++
	providerWindowBusName := ""
	providerWindowObjectPath := ""
	providerWindowProcessGeneration := ""
	providerWindowRole := ""
	providerWindowName := ""
	if len(providerIdentity) > 0 {
		providerWindowBusName = strings.TrimSpace(providerIdentity[0])
	}
	if len(providerIdentity) > 1 {
		providerWindowObjectPath = strings.TrimSpace(providerIdentity[1])
	}
	if len(providerIdentity) > 2 {
		providerWindowProcessGeneration = strings.TrimSpace(providerIdentity[2])
	}
	if len(providerIdentity) > 3 {
		providerWindowRole = normalizedTreeText(providerIdentity[3])
	}
	if len(providerIdentity) > 4 {
		providerWindowName = normalizedTreeText(providerIdentity[4])
	}
	appRef := ""
	if len(providerIdentity) > 5 {
		appRef = strings.TrimSpace(providerIdentity[5])
	}
	ownerIdentity := appIdentity
	if appRef != "" {
		ownerIdentity = appRef
	}
	key := windowRefIdentityKey(ownerIdentity, windowHandle)
	if ref := registry.byIdentity[key]; ref != "" {
		if entry := registry.entries[ref]; entry != nil {
			if entry.Lifecycle != AXRefActive &&
				entry.Lifecycle != AXRefNeedsRevalidation {
				delete(registry.byIdentity, key)
			} else if !windowProviderBindingContinuous(
				entry,
				providerWindowBusName,
				providerWindowObjectPath,
				providerWindowProcessGeneration,
				providerWindowRole,
				providerWindowName,
			) {
				entry.Lifecycle = AXRefStale
				entry.LifecycleReason = "logical_window_replaced"
				delete(registry.byIdentity, key)
			} else {
				entry.AppRef = appRef
				entry.SemanticTarget = semanticTarget
				entry.ProviderWindowBusName = providerWindowBusName
				entry.ProviderWindowObjectPath = providerWindowObjectPath
				entry.ProviderWindowProcessGeneration = providerWindowProcessGeneration
				entry.ProviderWindowRole = providerWindowRole
				entry.ProviderWindowName = providerWindowName
				entry.LastSeenRevision = revision
				entry.LastUsedTick = registry.tick
				entry.Lifecycle = AXRefActive
				entry.LifecycleReason = ""
				return ref
			}
		}
		delete(registry.byIdentity, key)
	}
	ref := "w" + strconv.FormatUint(registry.next, 10)
	registry.next++
	entry := &AXWindowRefEntry{
		Ref:                             ref,
		IdentityKey:                     key,
		AppRef:                          strings.TrimSpace(appRef),
		AppIdentity:                     appIdentity,
		WindowHandle:                    windowHandle,
		SemanticTarget:                  semanticTarget,
		ProviderWindowBusName:           providerWindowBusName,
		ProviderWindowObjectPath:        providerWindowObjectPath,
		ProviderWindowProcessGeneration: providerWindowProcessGeneration,
		ProviderWindowRole:              providerWindowRole,
		ProviderWindowName:              providerWindowName,
		CreatedRevision:                 revision,
		LastSeenRevision:                revision,
		LastUsedTick:                    registry.tick,
		Lifecycle:                       AXRefActive,
	}
	registry.entries[ref] = entry
	registry.byIdentity[key] = ref
	registry.evict()
	return ref
}

func (registry *WindowRefRegistry) evict() {
	for registry != nil && len(registry.entries) > registry.limit {
		oldestRef := ""
		var oldestTick uint64
		for ref, entry := range registry.entries {
			if oldestRef == "" || entry.LastUsedTick < oldestTick {
				oldestRef = ref
				oldestTick = entry.LastUsedTick
			}
		}
		entry := registry.entries[oldestRef]
		if entry == nil {
			delete(registry.entries, oldestRef)
			continue
		}
		delete(registry.entries, oldestRef)
		delete(registry.byIdentity, entry.IdentityKey)
	}
}

func parseMonotonicAXWindowRef(ref string) (uint64, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 2 || ref[0] != 'w' {
		return 0, false
	}
	value, err := strconv.ParseUint(ref[1:], 10, 64)
	return value, err == nil && value > 0
}

func (registry *WindowRefRegistry) Resolve(
	ref,
	requestedApp string,
) (*AXWindowRefEntry, error) {
	id, ok := parseMonotonicAXWindowRef(ref)
	if !ok {
		return nil, fmt.Errorf("invalid short AX window ref %q", ref)
	}
	if registry == nil {
		return nil, errors.New("AX window ref registry is unavailable")
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		if id < registry.next {
			return nil, fmt.Errorf(
				"short AX window ref %q expired from this session registry; observe the window again",
				ref,
			)
		}
		return nil, fmt.Errorf(
			"short AX window ref %q was never issued in this session",
			ref,
		)
	}
	if err := lifecycleResolveError(
		"window", ref, entry.Lifecycle, entry.LifecycleReason,
		"observe the window again",
	); err != nil {
		return nil, err
	}
	if requested := looseKey(requestedApp); requested != "" {
		registered := looseKey(entry.AppIdentity)
		if registered != "" &&
			!strings.Contains(registered, requested) &&
			!strings.Contains(requested, registered) {
			return nil, fmt.Errorf(
				"short AX window ref %q belongs to app %q, not %q",
				ref, entry.AppIdentity, requestedApp,
			)
		}
	}
	registry.tick++
	entry.LastUsedTick = registry.tick
	copy := *entry
	return &copy, nil
}

func (registry *WindowRefRegistry) MarkLifecycle(
	ref string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if registry == nil {
		return
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		return
	}
	entry.Lifecycle = lifecycle
	entry.LifecycleReason = strings.TrimSpace(reason)
	if lifecycle != AXRefActive && lifecycle != AXRefNeedsRevalidation {
		delete(registry.byIdentity, entry.IdentityKey)
	}
}

func (registry *WindowRefRegistry) MarkByApp(
	appRef string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if registry == nil {
		return
	}
	for ref, entry := range registry.entries {
		if entry != nil && entry.AppRef == strings.TrimSpace(appRef) {
			registry.MarkLifecycle(ref, lifecycle, reason)
		}
	}
}

func refIdentityKey(
	locatorKind AXLocatorKind,
	appIdentity,
	logicalWindowRef,
	windowHandle,
	semanticTarget string,
	role CanonicalRole,
	name,
	providerBusName,
	providerObjectPath,
	processGeneration,
	providerWindowBusName,
	providerWindowObjectPath,
	providerWindowProcessGeneration,
	providerIdentityScope string,
) string {
	if locatorKind == AXLocatorProviderBound &&
		strings.TrimSpace(providerBusName) != "" &&
		strings.TrimSpace(providerObjectPath) != "" &&
		strings.TrimSpace(processGeneration) != "" {
		windowIdentity := strings.TrimSpace(logicalWindowRef)
		if windowIdentity == "" {
			windowIdentity = strings.TrimSpace(windowHandle)
		}
		parts := []string{
			string(locatorKind),
			strings.ToLower(strings.TrimSpace(appIdentity)),
			windowIdentity,
			strings.TrimSpace(processGeneration),
			strings.TrimSpace(providerBusName),
			strings.TrimSpace(providerObjectPath),
			strings.TrimSpace(providerIdentityScope),
		}
		if strings.TrimSpace(providerIdentityScope) == "table_owner" {
			parts = append(parts, strings.TrimSpace(semanticTarget))
		}
		return strings.Join(parts, "\x00")
	}
	return strings.Join([]string{
		string(locatorKind),
		strings.ToLower(strings.TrimSpace(appIdentity)),
		firstNonEmpty(strings.TrimSpace(logicalWindowRef), strings.TrimSpace(windowHandle)),
		strings.TrimSpace(semanticTarget),
	}, "\x00")
}

func (registry *RefRegistry) AcquireTyped(
	locatorKind AXLocatorKind,
	appIdentity,
	windowHandle,
	semanticTarget string,
	role CanonicalRole,
	name,
	revision string,
	providerIdentity ...string,
) string {
	if registry == nil || strings.TrimSpace(windowHandle) == "" {
		return ""
	}
	if locatorKind == "" {
		locatorKind = AXLocatorSemantic
	}
	providerBusName := ""
	providerObjectPath := ""
	processGeneration := ""
	providerWindowBusName := ""
	providerWindowObjectPath := ""
	providerWindowProcessGeneration := ""
	providerWindowRole := ""
	providerWindowName := ""
	if len(providerIdentity) > 0 {
		providerBusName = providerIdentity[0]
	}
	if len(providerIdentity) > 1 {
		providerObjectPath = providerIdentity[1]
	}
	if len(providerIdentity) > 2 {
		processGeneration = providerIdentity[2]
	}
	if len(providerIdentity) > 3 {
		providerWindowBusName = providerIdentity[3]
	}
	if len(providerIdentity) > 4 {
		providerWindowObjectPath = providerIdentity[4]
	}
	if len(providerIdentity) > 5 {
		providerWindowProcessGeneration = providerIdentity[5]
	}
	if len(providerIdentity) > 6 {
		providerWindowRole = providerIdentity[6]
	}
	if len(providerIdentity) > 7 {
		providerWindowName = providerIdentity[7]
	}
	appRef := ""
	if len(providerIdentity) > 8 {
		appRef = strings.TrimSpace(providerIdentity[8])
	}
	logicalWindowRef := ""
	if len(providerIdentity) > 9 {
		logicalWindowRef = strings.TrimSpace(providerIdentity[9])
	}
	providerIdentityScope := ""
	if len(providerIdentity) > 10 {
		providerIdentityScope = strings.TrimSpace(providerIdentity[10])
	}
	providerComplete := strings.TrimSpace(providerBusName) != "" &&
		strings.TrimSpace(providerObjectPath) != "" &&
		strings.TrimSpace(processGeneration) != ""
	if locatorKind == AXLocatorProviderBound && !providerComplete {
		return ""
	}
	if locatorKind != AXLocatorProviderBound &&
		strings.TrimSpace(semanticTarget) == "" {
		return ""
	}
	registry.tick++
	ownerIdentity := appIdentity
	if appRef != "" {
		ownerIdentity = appRef
	}
	key := refIdentityKey(
		locatorKind, ownerIdentity, logicalWindowRef, windowHandle,
		semanticTarget, role, name,
		providerBusName, providerObjectPath, processGeneration,
		providerWindowBusName, providerWindowObjectPath,
		providerWindowProcessGeneration, providerIdentityScope,
	)
	if ref := registry.byIdentity[key]; ref != "" {
		if entry := registry.entries[ref]; entry != nil {
			if entry.Lifecycle != AXRefActive &&
				entry.Lifecycle != AXRefNeedsRevalidation {
				delete(registry.byIdentity, key)
			} else {
				if locatorKind == AXLocatorProviderBound &&
					providerComplete &&
					!providerSemanticContinuity(entry, role, name, semanticTarget) {
					entry.Lifecycle = AXRefSuperseded
					entry.StaleReason = "provider identity now exposes a different semantic object"
					delete(registry.byIdentity, key)
				} else {
					entry.AppRef = appRef
					entry.LogicalWindowRef = logicalWindowRef
					providerChanged := locatorKind == AXLocatorSemantic &&
						completeProviderIdentityChanged(
							entry,
							providerBusName,
							providerObjectPath,
							processGeneration,
						)
					if !providerChanged {
						entry.ProviderBusName = strings.TrimSpace(providerBusName)
						entry.ProviderObjectPath = strings.TrimSpace(providerObjectPath)
						entry.ProcessGeneration = strings.TrimSpace(processGeneration)
						entry.ProviderIdentityScope = providerIdentityScope
					}
					entry.LastSeenRevision = revision
					entry.LastUsedTick = registry.tick
					entry.ProviderWindowRole = normalizedTreeText(providerWindowRole)
					entry.ProviderWindowName = normalizedTreeText(providerWindowName)
					entry.Lifecycle = AXRefActive
					entry.StaleReason = ""
					return ref
				}
			}
		}
		delete(registry.byIdentity, key)
	}
	ref := "r" + strconv.FormatUint(registry.next, 10)
	registry.next++
	entry := &AXRefEntry{
		Ref:                             ref,
		IdentityKey:                     key,
		LocatorKind:                     locatorKind,
		Lifecycle:                       AXRefActive,
		AppRef:                          strings.TrimSpace(appRef),
		AppIdentity:                     strings.ToLower(strings.TrimSpace(appIdentity)),
		LogicalWindowRef:                logicalWindowRef,
		WindowHandle:                    strings.TrimSpace(windowHandle),
		ProviderBusName:                 strings.TrimSpace(providerBusName),
		ProviderObjectPath:              strings.TrimSpace(providerObjectPath),
		ProcessGeneration:               strings.TrimSpace(processGeneration),
		ProviderIdentityScope:           providerIdentityScope,
		ProviderWindowBusName:           strings.TrimSpace(providerWindowBusName),
		ProviderWindowObjectPath:        strings.TrimSpace(providerWindowObjectPath),
		ProviderWindowProcessGeneration: strings.TrimSpace(providerWindowProcessGeneration),
		ProviderWindowRole:              normalizedTreeText(providerWindowRole),
		ProviderWindowName:              normalizedTreeText(providerWindowName),
		SemanticTarget:                  strings.TrimSpace(semanticTarget),
		Role:                            role,
		Name:                            normalizedTreeText(name),
		CreatedRevision:                 revision,
		LastSeenRevision:                revision,
		LastUsedTick:                    registry.tick,
	}
	registry.entries[ref] = entry
	registry.byIdentity[key] = ref
	registry.evict()
	return ref
}

func completeProviderIdentityChanged(
	entry *AXRefEntry,
	providerBusName,
	providerObjectPath,
	processGeneration string,
) bool {
	if entry == nil {
		return false
	}
	oldComplete := strings.TrimSpace(entry.ProviderBusName) != "" &&
		strings.TrimSpace(entry.ProviderObjectPath) != "" &&
		strings.TrimSpace(entry.ProcessGeneration) != ""
	newComplete := strings.TrimSpace(providerBusName) != "" &&
		strings.TrimSpace(providerObjectPath) != "" &&
		strings.TrimSpace(processGeneration) != ""
	if !oldComplete || !newComplete {
		return false
	}
	return entry.ProviderBusName != strings.TrimSpace(providerBusName) ||
		entry.ProviderObjectPath != strings.TrimSpace(providerObjectPath) ||
		entry.ProcessGeneration != strings.TrimSpace(processGeneration)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func providerSemanticContinuity(
	entry *AXRefEntry,
	role CanonicalRole,
	name,
	semanticTarget string,
) bool {
	if entry == nil {
		return false
	}
	if entry.Role != "" && role != "" && entry.Role != role {
		return false
	}
	if comparableTreeText(entry.Name) != comparableTreeText(name) {
		return false
	}
	return strings.TrimSpace(entry.SemanticTarget) ==
		strings.TrimSpace(semanticTarget)
}

func runtimePathEqual(first, second []int) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func runtimePathMatchesEntry(entry *AXRefEntry, runtimeID []int) bool {
	if entry == nil {
		return false
	}
	if runtimePathEqual(entry.RuntimeID, runtimeID) {
		return true
	}
	for _, alias := range entry.RuntimeIDAliases {
		if runtimePathEqual(alias, runtimeID) {
			return true
		}
	}
	return false
}

func (registry *RefRegistry) AcquireNode(
	snapshot *AXSnapshotV1,
	node *AXNode,
	locatorKind AXLocatorKind,
) string {
	if registry == nil || snapshot == nil || node == nil {
		return ""
	}
	if locatorKind == AXLocatorSemantic &&
		snapshot.IdentityCoverageIncomplete {
		if !axNodeProviderIdentityComplete(node) {
			return ""
		}
		locatorKind = AXLocatorProviderBound
	}
	if locatorKind == AXLocatorSemantic &&
		!snapshot.IdentityCoverageIncomplete {
		key := refIdentityKey(
			locatorKind,
			firstNonEmpty(snapshot.AppRef, snapshot.AppIdentity),
			snapshot.WindowRef,
			snapshot.WindowHandle,
			node.SemanticTarget,
			node.Role,
			node.Name,
			node.ProviderBusName,
			node.ProviderObjectPath,
			node.ProcessGeneration,
			node.ProviderWindowBusName,
			node.ProviderWindowObjectPath,
			node.ProviderWindowProcessGeneration,
			node.ProviderIdentityScope,
		)
		if existingRef := registry.byIdentity[key]; existingRef != "" {
			entry := registry.entries[existingRef]
			if completeProviderIdentityChanged(
				entry,
				node.ProviderBusName,
				node.ProviderObjectPath,
				node.ProcessGeneration,
			) && !runtimePathMatchesEntry(entry, node.RuntimeID) {
				// A complete fresh snapshot must never advertise an rN that the
				// live resolver will immediately reject. A different provider
				// identity outside the recorded runtime lineage is a replacement,
				// not proven continuity: retire the old ref and issue a new one.
				registry.MarkSuperseded(
					existingRef,
					"complete snapshot observed a replacement provider lineage",
				)
			}
		}
	}
	ref := registry.AcquireTyped(
		locatorKind,
		snapshot.AppIdentity,
		snapshot.WindowHandle,
		node.SemanticTarget,
		node.Role,
		node.Name,
		snapshot.Revision,
		node.ProviderBusName,
		node.ProviderObjectPath,
		node.ProcessGeneration,
		node.ProviderWindowBusName,
		node.ProviderWindowObjectPath,
		node.ProviderWindowProcessGeneration,
		node.ProviderWindowRole,
		node.ProviderWindowName,
		snapshot.AppRef,
		snapshot.WindowRef,
		node.ProviderIdentityScope,
	)
	if ref != "" {
		entry := registry.entries[ref]
		if locatorKind == AXLocatorSemantic &&
			completeProviderIdentityChanged(
				entry,
				node.ProviderBusName,
				node.ProviderObjectPath,
				node.ProcessGeneration,
			) {
			// A fresh snapshot may observe a rebuilt provider wrapper before
			// the action resolver has proved object continuity. Preserve the
			// original runtime-path lineage until that live, window-scoped
			// check succeeds. Otherwise a later same-name peer could replace
			// the evidence needed to reject an unsafe rebind.
			return ref
		}
		registry.BindRuntimePaths(ref, node.RuntimeID, node.RuntimeIDAliases)
	}
	return ref
}

func (registry *RefRegistry) MarkStale(ref, reason string) {
	registry.MarkLifecycle(ref, AXRefStale, reason)
}

func (registry *RefRegistry) MarkNeedsRevalidation(ref, reason string) {
	registry.MarkLifecycle(ref, AXRefNeedsRevalidation, reason)
}

func (registry *RefRegistry) MarkSuperseded(ref, reason string) {
	registry.MarkLifecycle(ref, AXRefSuperseded, reason)
}

func (registry *RefRegistry) MarkLifecycle(
	ref string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if registry == nil {
		return
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil || entry.Lifecycle == AXRefExpired {
		return
	}
	entry.Lifecycle = lifecycle
	entry.StaleReason = strings.TrimSpace(reason)
	if lifecycle != AXRefActive && lifecycle != AXRefNeedsRevalidation {
		delete(registry.byIdentity, entry.IdentityKey)
	}
}

func (registry *RefRegistry) MarkByApp(
	appRef string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if registry == nil {
		return
	}
	for ref, entry := range registry.entries {
		if entry != nil && entry.AppRef == strings.TrimSpace(appRef) {
			registry.MarkLifecycle(ref, lifecycle, reason)
		}
	}
}

func (registry *RefRegistry) MarkByWindow(
	windowRef string,
	lifecycle AXRefLifecycle,
	reason string,
) {
	if registry == nil {
		return
	}
	for ref, entry := range registry.entries {
		if entry != nil &&
			entry.LogicalWindowRef == strings.TrimSpace(windowRef) {
			registry.MarkLifecycle(ref, lifecycle, reason)
		}
	}
}

func (registry *RefRegistry) evict() {
	for registry != nil && len(registry.entries) > registry.limit {
		oldestRef := ""
		var oldestTick uint64
		for ref, entry := range registry.entries {
			if oldestRef == "" || entry.LastUsedTick < oldestTick {
				oldestRef = ref
				oldestTick = entry.LastUsedTick
			}
		}
		entry := registry.entries[oldestRef]
		if entry == nil {
			delete(registry.entries, oldestRef)
			continue
		}
		delete(registry.entries, oldestRef)
		delete(registry.byIdentity, entry.IdentityKey)
		entry.Lifecycle = AXRefExpired
		entry.StaleReason = "session registry capacity eviction"
	}
}

func parseMonotonicAXRef(ref string) (uint64, bool) {
	ref = strings.TrimSpace(ref)
	if len(ref) < 2 || ref[0] != 'r' {
		return 0, false
	}
	value, err := strconv.ParseUint(ref[1:], 10, 64)
	return value, err == nil && value > 0
}

func (registry *RefRegistry) Resolve(
	ref,
	requestedApp string,
) (*AXRefEntry, error) {
	id, ok := parseMonotonicAXRef(ref)
	if !ok {
		return nil, fmt.Errorf("invalid short AX ref %q", ref)
	}
	if registry == nil {
		return nil, errors.New("AX ref registry is unavailable")
	}
	entry := registry.entries[strings.TrimSpace(ref)]
	if entry == nil {
		if id < registry.next {
			return nil, fmt.Errorf(
				"short AX ref %q expired from this session registry; observe or find the target again",
				ref,
			)
		}
		return nil, fmt.Errorf(
			"short AX ref %q was never issued in this session",
			ref,
		)
	}
	if entry.Lifecycle != AXRefActive {
		reason := "target identity is no longer current"
		switch entry.Lifecycle {
		case AXRefNeedsRevalidation:
			reason = "target identity requires a fresh observation"
		case AXRefSuperseded:
			reason = "target identity was replaced"
		case AXRefExpired:
			reason = "target reference expired"
		}
		return nil, fmt.Errorf(
			"short AX ref %q is %s (%s); observe or find the target again",
			ref, entry.Lifecycle, reason,
		)
	}
	if requested := looseKey(requestedApp); requested != "" {
		registered := looseKey(entry.AppIdentity)
		if registered != "" &&
			!strings.Contains(registered, requested) &&
			!strings.Contains(requested, registered) {
			return nil, fmt.Errorf(
				"short AX ref %q belongs to app %q, not %q",
				ref, entry.AppIdentity, requestedApp,
			)
		}
	}
	registry.tick++
	entry.LastUsedTick = registry.tick
	copy := *entry
	return &copy, nil
}

func axNodeProviderIdentityComplete(node *AXNode) bool {
	return node != nil &&
		strings.TrimSpace(node.ProviderBusName) != "" &&
		strings.TrimSpace(node.ProviderObjectPath) != "" &&
		strings.TrimSpace(node.ProcessGeneration) != ""
}

func axNodeSemanticIdentityComplete(node *AXNode) bool {
	return node != nil &&
		node.LocatorKind == AXLocatorSemantic &&
		strings.TrimSpace(node.SemanticTarget) != ""
}

func axNodeFindIdentityKey(node *AXNode) string {
	if node == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(node.Role))) + "\x00" +
		normalizedTreeText(node.Name)
}

func axNodeHasReliableGeometry(node *AXNode) bool {
	return node != nil &&
		node.SourceRecord != nil &&
		node.SourceRecord.Frame != nil &&
		node.SourceRecord.Frame.Width > 0 &&
		node.SourceRecord.Frame.Height > 0
}

func axNodeHasPointerEvidence(node *AXNode) bool {
	if node == nil || !axNodeHasReliableGeometry(node) {
		return false
	}
	for _, state := range recordStateTokens(node.SourceRecord) {
		if state == "has-click-action" {
			return true
		}
	}
	return false
}

func axNodeHasInteractionSemantics(node *AXNode) bool {
	if node == nil {
		return false
	}
	if axNodeHasCapability(node) || axNodeHasPointerEvidence(node) {
		return true
	}
	switch node.SemanticClass {
	case "control":
		return true
	case "item":
		// Desktop providers frequently omit action metadata for selectable
		// cells, tabs and tree/list items. Reliable visible geometry is enough
		// to issue an identity ref; action support is checked at execution.
		return axNodeHasReliableGeometry(node)
	default:
		return false
	}
}

func axNodeRefEligibility(
	snapshot *AXSnapshotV1,
	node *AXNode,
) (bool, string, AXLocatorKind) {
	if snapshot == nil || node == nil {
		return false, "provider_identity_incomplete", ""
	}
	if strings.TrimSpace(snapshot.AppRef) == "" ||
		strings.TrimSpace(snapshot.WindowRef) == "" {
		return false, "owner_identity_incomplete", ""
	}

	locatorKind := node.LocatorKind
	if locatorKind == "" && node.SemanticTarget != "" {
		locatorKind = AXLocatorSemantic
	}
	if snapshot.IdentityCoverageIncomplete {
		if axNodeProviderIdentityComplete(node) {
			locatorKind = AXLocatorProviderBound
		} else {
			return false, "identity_coverage_incomplete", ""
		}
	}

	for parent := node.Parent; parent != nil; parent = parent.Parent {
		if parent.States.Expanded == TriFalse {
			return false, "hidden_under_collapsed_ancestor", ""
		}
	}
	if node.Showing != nil && !*node.Showing && !node.ForceKeep {
		return false, "not_currently_showing", ""
	}
	if node.Visible != nil && !*node.Visible && !node.ForceKeep {
		return false, "not_currently_visible", ""
	}

	// A find result is an explicit request to address this exact canonical
	// object. Identity and actionability are separate: reuse a unique,
	// window-scoped semantic locator when available, otherwise use a complete
	// provider identity. Action support is checked before execution.
	if node.FindMatch {
		if axNodeSemanticIdentityComplete(node) &&
			!node.SemanticIdentityAmbiguous {
			return true, "", AXLocatorSemantic
		}
		if axNodeProviderIdentityComplete(node) {
			return true, "", AXLocatorProviderBound
		}
		return false, "stable_identity_unavailable", ""
	}

	if !axNodeHasInteractionSemantics(node) {
		return false, "interaction_semantics_unknown", ""
	}
	if locatorKind == "" {
		return false, "semantic_identity_ambiguous", ""
	}
	if strings.TrimSpace(node.Name) == "" &&
		strings.TrimSpace(node.Properties.Description) == "" &&
		strings.TrimSpace(node.Properties.Placeholder) == "" {
		return false, "unnamed_target", ""
	}
	return true, "", locatorKind
}

func (registry *RefRegistry) AssignSnapshotRefs(snapshot *AXSnapshotV1) {
	if registry == nil || snapshot == nil {
		return
	}
	seenRefs := map[string]bool{}
	semanticCounts := map[string]int{}
	findIdentityCounts := map[string]int{}
	var countFindTargets func([]*AXNode)
	countFindTargets = func(nodes []*AXNode) {
		for _, node := range nodes {
			if node.FindMatch && axNodeSemanticIdentityComplete(node) {
				semanticCounts[strings.TrimSpace(node.SemanticTarget)]++
			}
			if node.FindMatch {
				findIdentityCounts[axNodeFindIdentityKey(node)]++
			}
			countFindTargets(node.Children)
		}
	}
	countFindTargets(snapshot.Roots)

	var visit func([]*AXNode)
	visit = func(nodes []*AXNode) {
		for _, node := range nodes {
			node.SemanticIdentityAmbiguous =
				node.FindMatch &&
					(semanticCounts[strings.TrimSpace(node.SemanticTarget)] > 1 ||
						findIdentityCounts[axNodeFindIdentityKey(node)] > 1)
			if node.FindMatch {
				// Find context clones may inherit a ref issued for the broader
				// working tree. Re-evaluate addressability in the exact match
				// set so an ambiguous or identity-incomplete candidate is
				// never advertised as copyable.
				node.Ref = ""
			}
			node.AddressabilityEvaluated = true
			node.AddressabilityReason = ""
			if canonical := snapshot.ByID[node.ID]; canonical != nil {
				canonical.AddressabilityEvaluated = true
				canonical.AddressabilityReason = ""
			}
			registry.markExistingNodeRefsPresent(snapshot, node, seenRefs)
			if eligible, reason, locatorKind := axNodeRefEligibility(
				snapshot, node,
			); eligible {
				node.LocatorKind = locatorKind
				node.Ref = registry.AcquireNode(
					snapshot,
					node,
					locatorKind,
				)
				if canonical := snapshot.ByID[node.ID]; canonical != nil {
					canonical.Ref = node.Ref
					canonical.LocatorKind = locatorKind
				}
				if node.Ref != "" {
					seenRefs[node.Ref] = true
				}
			} else {
				node.AddressabilityReason = reason
				if canonical := snapshot.ByID[node.ID]; canonical != nil {
					canonical.AddressabilityReason = reason
				}
			}
			visit(node.Children)
		}
	}
	visit(snapshot.Roots)
	if !snapshot.RefInventoryComplete {
		return
	}
	windowRef := strings.TrimSpace(snapshot.WindowRef)
	for ref, entry := range registry.entries {
		if entry == nil ||
			entry.LogicalWindowRef != windowRef ||
			entry.Lifecycle == AXRefExpired ||
			seenRefs[ref] {
			continue
		}
		registry.MarkSuperseded(
			ref,
			"absent from complete canonical window inventory",
		)
	}
}

func (registry *RefRegistry) markExistingNodeRefsPresent(
	snapshot *AXSnapshotV1,
	node *AXNode,
	seenRefs map[string]bool,
) {
	if registry == nil || snapshot == nil || node == nil || seenRefs == nil {
		return
	}
	ownerIdentity := firstNonEmpty(snapshot.AppRef, snapshot.AppIdentity)
	mark := func(locatorKind AXLocatorKind) {
		key := refIdentityKey(
			locatorKind,
			ownerIdentity,
			snapshot.WindowRef,
			snapshot.WindowHandle,
			node.SemanticTarget,
			node.Role,
			node.Name,
			node.ProviderBusName,
			node.ProviderObjectPath,
			node.ProcessGeneration,
			node.ProviderWindowBusName,
			node.ProviderWindowObjectPath,
			node.ProviderWindowProcessGeneration,
			node.ProviderIdentityScope,
		)
		ref := registry.byIdentity[key]
		entry := registry.entries[ref]
		if entry == nil || entry.Lifecycle == AXRefExpired {
			return
		}
		if locatorKind == AXLocatorSemantic &&
			completeProviderIdentityChanged(
				entry,
				node.ProviderBusName,
				node.ProviderObjectPath,
				node.ProcessGeneration,
			) &&
			!runtimePathMatchesEntry(entry, node.RuntimeID) {
			return
		}
		seenRefs[ref] = true
		entry.LastSeenRevision = snapshot.Revision
	}
	if strings.TrimSpace(node.SemanticTarget) != "" {
		mark(AXLocatorSemantic)
	}
	if axNodeProviderIdentityComplete(node) {
		mark(AXLocatorProviderBound)
	}
}
