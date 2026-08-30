package main

func providerObserveAppsRequestFromOperation(
	axServiceRequest,
) ProviderObserveAppsRequest {
	return ProviderObserveAppsRequest{ProtocolVersion: providerProtocolV2}
}

func providerObserveRequestFromOperation(
	operation axServiceRequest,
) ProviderObserveRequest {
	return ProviderObserveRequest{
		ProtocolVersion: providerProtocolV2,
		App:             operation.App,
		AppIdentity:     operation.AppIdentity,
		WindowHandle:    operation.TargetWindowHandle,
		Observation:     providerObservationOptionsFromOperation(operation),
		KnownRefs:       cloneProviderRefs(operation.KnownRefs),
	}
}

func providerActionRequestFromOperation(
	operation axServiceRequest,
) ProviderActionRequest {
	if operation.Action == nil {
		panic("provider action request requires a typed action command")
	}
	request := ProviderActionRequest{
		ProtocolVersion: providerProtocolV2,
		Intent:          operation.Action.Intent(),
		App:             operation.App,
		AppIdentity:     operation.AppIdentity,
		WindowHandle:    operation.TargetWindowHandle,
		ExpectedWindow:  operation.ExpectedWindow,
		Element:         operation.Element,
		Target:          providerTargetFromOperation(operation.SemanticTarget),
		WindowBounds:    operation.WindowBounds,
		Observation:     providerObservationOptionsFromOperation(operation),
		KnownRefs:       cloneProviderRefs(operation.KnownRefs),
	}
	operation.Action.applyProviderAction(&request)
	return request
}

func providerWindowListRequestFromOperation(
	operation axServiceRequest,
) ProviderWindowListRequest {
	return ProviderWindowListRequest{
		ProtocolVersion: providerProtocolV2,
		App:             operation.App,
		AppIdentity:     operation.AppIdentity,
		WindowHandle:    operation.TargetWindowHandle,
	}
}

func providerWindowActionRequestFromOperation(
	operation axServiceRequest,
) ProviderWindowActionRequest {
	if operation.WindowAction == nil {
		panic("provider window action request requires a typed window action")
	}
	return ProviderWindowActionRequest{
		ProtocolVersion: providerProtocolV2,
		Action:          *operation.WindowAction,
		App:             operation.App,
		AppIdentity:     operation.AppIdentity,
		WindowHandle:    operation.TargetWindowHandle,
		Target:          providerTargetFromOperation(operation.SemanticTarget),
		Observation:     providerObservationOptionsFromOperation(operation),
		KnownRefs:       cloneProviderRefs(operation.KnownRefs),
	}
}

func providerObservationOptionsFromOperation(
	operation axServiceRequest,
) ProviderObservationOptions {
	var captureSnapshot *bool
	if operation.WindowAction != nil {
		// Window actions require an internal post-action snapshot for native
		// lifecycle verification even when the caller selected observe=none.
		// Observe controls model-visible output, not the evidence required to
		// prove activation or closure.
		capture := true
		captureSnapshot = &capture
	} else if operation.CaptureSnapshot != nil {
		capture := *operation.CaptureSnapshot
		captureSnapshot = &capture
	} else if operation.Action != nil {
		capture := (observation{mode: operation.Observe}).resolvedMode() !=
			observeNone
		captureSnapshot = &capture
	}
	return ProviderObservationOptions{
		TextLimit:         providerTextLimit(operation.TextLimit),
		MaxTreeNodes:      operation.MaxTreeNodes,
		MaxTreeDepth:      operation.MaxTreeDepth,
		Prune:             operation.Prune,
		Boxes:             operation.Boxes,
		IncludeScreenshot: operation.IncludeScreenshot,
		ScreenshotScope:   operation.ScreenshotScope,
		CaptureSnapshot:   captureSnapshot,
	}
}

func providerTextLimit(value any) *ProviderTextLimit {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if typed == "max" {
			return &ProviderTextLimit{Max: true}
		}
	case int:
		if typed > 0 {
			return &ProviderTextLimit{Value: typed}
		}
	case float64:
		if typed > 0 && typed == float64(int(typed)) {
			return &ProviderTextLimit{Value: int(typed)}
		}
	}
	return nil
}

func cloneProviderRefs(
	refs map[string]elementRef,
) map[string]ProviderElementRef {
	if len(refs) == 0 {
		return nil
	}
	cloned := make(map[string]ProviderElementRef, len(refs))
	for key, ref := range refs {
		cloned[key] = ref
	}
	return cloned
}

func providerTargetFromOperation(
	target *semanticTarget,
) *ProviderSemanticTarget {
	if target == nil {
		return nil
	}
	return &ProviderSemanticTarget{
		Selector:                        target.Selector,
		LocatorKind:                     target.LocatorKind,
		AllowProviderRebind:             target.AllowProviderRebind,
		WindowHandle:                    target.WindowHandle,
		ProviderBusName:                 target.ProviderBusName,
		ProviderObjectPath:              target.ProviderObjectPath,
		ProcessGeneration:               target.ProcessGeneration,
		ProviderWindowBusName:           target.ProviderWindowBusName,
		ProviderWindowObjectPath:        target.ProviderWindowObjectPath,
		ProviderWindowProcessGeneration: target.ProviderWindowProcessGeneration,
		ProviderWindowRole:              target.ProviderWindowRole,
		ProviderWindowName:              target.ProviderWindowName,
		ProviderRoleAliases:             append([]string(nil), target.ProviderRoleAliases...),
		ProviderIdentityScope:           target.ProviderIdentityScope,
		RuntimeID:                       append([]int(nil), target.RuntimeID...),
		RuntimeIDAliases:                append([][]int(nil), target.RuntimeIDAliases...),
		Role:                            target.Role,
		Name:                            target.Name,
		HasName:                         target.HasName,
		Predicates:                      append([]semanticTargetPredicate(nil), target.Predicates...),
		SourceIndex:                     target.SourceIndex,
	}
}
