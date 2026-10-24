package core

// publicKeyboardDelivery exposes queue counts without native routing or IDs.
// Queue removal cannot establish that the application handled the input.
func publicKeyboardDelivery(value any) map[string]any {
	var evidence *RuntimeKeyboardQueueEvidence
	switch value := value.(type) {
	case RuntimeActionEvidence:
		evidence = value.RuntimeKeyboardQueueEvidence
	case *RuntimeActionEvidence:
		if value != nil {
			evidence = value.RuntimeKeyboardQueueEvidence
		}
	}
	if evidence == nil {
		return nil
	}
	return map[string]any{
		"requested_events": evidence.RequestedEvents,
		"consumed_events":  evidence.ConsumedEvents,
		"effect_verified":  false,
	}
}
