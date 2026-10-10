package provider

import (
	"encoding/json"
	"errors"
)

// suspensionDrift compensates for servers that omit suspension from their plan.
// Only equal service sets qualify. Additions and removals use the server plan.
// The caller supplies normalized JSON, so env_files never requires a plan-time import.
func suspensionDrift(requested, observed json.RawMessage) (bool, error) {
	a, err := suspensionStates(requested)
	if err != nil {
		return false, err
	}
	b, err := suspensionStates(observed)
	if err != nil {
		return false, err
	}
	if len(a) != len(b) {
		return false, nil
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			return false, nil
		}
	}
	for name, suspended := range a {
		if suspended != b[name] {
			return true, nil
		}
	}
	return false, nil
}

func suspensionStates(raw json.RawMessage) (map[string]bool, error) {
	invalid := errors.New("APPLICATION_SUSPENSION_SPEC_INVALID: The application spec must contain service objects with boolean suspension values.")
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil || doc == nil {
		return nil, invalid
	}
	services := map[string]json.RawMessage{}
	if value, ok := doc["services"]; ok {
		if json.Unmarshal(value, &services) != nil || services == nil {
			return nil, invalid
		}
	}
	states := make(map[string]bool, len(services))
	for name, rawService := range services {
		var service map[string]json.RawMessage
		if json.Unmarshal(rawService, &service) != nil || service == nil {
			return nil, invalid
		}
		var suspended bool
		if value, ok := service["suspended"]; ok {
			var parsed any
			if json.Unmarshal(value, &parsed) != nil {
				return nil, invalid
			}
			var valid bool
			suspended, valid = parsed.(bool)
			if !valid {
				return nil, invalid
			}
		}
		states[name] = suspended
	}
	return states, nil
}
