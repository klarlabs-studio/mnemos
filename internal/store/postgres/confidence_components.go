package postgres

import "encoding/json"

// encodeConfidenceComponents serializes a claim's trust-component map to the JSON
// text stored in the confidence_components column. An empty/nil map encodes as the
// empty object so the column is never NULL. Mirrors the sqlite backend so the
// credit-assignment (ADR 0014) and salience (ADR 0013) audit maps round-trip
// identically on the hosted backend.
func encodeConfidenceComponents(components map[string]float64) string {
	if len(components) == 0 {
		return "{}"
	}
	b, err := json.Marshal(components)
	if err != nil {
		// A map[string]float64 cannot fail to marshal; fall back to the empty
		// object so a corrupt map can never poison the upsert.
		return "{}"
	}
	return string(b)
}

// decodeConfidenceComponents parses the confidence_components column back into a
// map, returning nil for empty/absent/unparseable values.
func decodeConfidenceComponents(raw string) map[string]float64 {
	if raw == "" || raw == "{}" {
		return nil
	}
	out := map[string]float64{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
