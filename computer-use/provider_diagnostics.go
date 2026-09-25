package main

type ProviderDiagnosticContext struct {
	PhaseTiming bool `json:"phase_timing,omitempty"`
}

func providerDiagnosticContextFromLookup(lookup func(string) string) *ProviderDiagnosticContext {
	if lookup == nil || lookup("COMPUTER_USE_DIAGNOSTIC_TIMING") != "1" {
		return nil
	}
	return &ProviderDiagnosticContext{PhaseTiming: true}
}

func providerTargetDiagnosticEnabled(lookup func(string) string) bool {
	return lookup != nil && lookup("COMPUTER_USE_DIAGNOSTIC_TARGET_RESOLUTION") == "1"
}
