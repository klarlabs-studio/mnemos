package sqlite

import "encoding/json"

// encodeConfidenceComponents renders the in-memory components map into
// the JSON form persisted in the claims.confidence_components column.
// Nil or empty maps round-trip as "{}" so the column never holds NULL
// (the schema enforces NOT NULL DEFAULT '{}').
func encodeConfidenceComponents(components map[string]float64) string {
	if len(components) == 0 {
		return "{}"
	}
	b, err := json.Marshal(components)
	if err != nil {
		// A map[string]float64 can't fail to marshal; if Go ever
		// changes that, fall back to the empty object so a corrupt
		// component map can't poison the upsert.
		return "{}"
	}
	return string(b)
}

// decodeConfidenceComponents parses a column value back into the
// map. Empty / "{}" returns nil so callers can distinguish "no
// decomposition" from "decomposition with zero entries". Malformed
// JSON also returns nil — the cost of a "missing" component is lower
// than crashing a read path on a stray row.
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
