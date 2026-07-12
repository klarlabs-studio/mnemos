package mysql

import "encoding/json"

// encodeConfidenceComponents serializes a claim's trust-component map to the JSON
// text stored in the confidence_components column (empty/nil → "{}"), mirroring
// the sqlite/postgres backends so the credit-assignment (ADR 0014) and salience
// (ADR 0013) audit maps round-trip identically on MySQL.
func encodeConfidenceComponents(components map[string]float64) string {
	if len(components) == 0 {
		return "{}"
	}
	b, err := json.Marshal(components)
	if err != nil {
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
