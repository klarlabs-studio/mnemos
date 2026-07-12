package consolidate

// Local float-back — the upward flow that lets important learnings in a
// repo/workspace brain (a "sub-region") float up into the user's personal
// central brain. It is the LOCAL twin of the hosted tenant→global promotion in
// promote.go: same topology (sub-region → central brain), a different gate.
//
// The hosted gate (promote.go) is PRIVACY: many owners share a global tier, so
// a learning must be cross-tenant corroborated and de-identified before it can
// leak upward. Local float-back has a SINGLE owner — there is nothing to
// de-identify and no corroboration requirement. Its gate is instead
// IMPORTANCE / GENERALITY: does this learning apply beyond the one repo it was
// captured in? Plus an explicit "remember globally" escape hatch that floats a
// claim up unconditionally.
//
// This engine is pure and I/O-free: the caller supplies source claims with
// their computed trust scores and the set of statements already present
// centrally; PlanFloatBack returns a fully auditable plan (what floats, what is
// skipped and why). That keeps every gate unit-testable without a live store.

import "strings"

// FloatGlobalComponent is the reserved [go.klarlabs.de/mnemos/internal/domain.Belief.ConfidenceComponents]
// key that marks a claim as "remember globally". It is set by
// `mnemos claim record --global`. A claim carrying this key (value > 0) floats
// up to the central brain unconditionally — it bypasses the trust and
// generality gates, but never the dedup or active-status gates.
const FloatGlobalComponent = "float:global"

// DefaultMinTrust is the trust-score floor a claim must clear to float up on the
// generality path. Deliberately a high bar: only well-corroborated, fresh
// learnings are worth promoting into the personal central brain.
const DefaultMinTrust = 0.6

// Float / skip reasons (machine-stable, surfaced verbatim in the plan JSON).
const (
	ReasonFloatExplicitGlobal = "explicitly tagged remember-globally (--global)"
	ReasonFloatGeneral        = "general, high-trust learning"
	ReasonSkipInactive        = "claim is not active"
	ReasonSkipDuplicate       = "already present in the central brain"
	ReasonSkipBelowTrust      = "trust score below the float threshold"
	ReasonSkipRepoLocal       = "repo-specific (path/name/sha-bound), not generally applicable"
	ReasonSkipEmpty           = "statement is empty after stripping repo-specifics"
)

// FloatInput is one source claim paired with its caller-computed trust score.
// It mirrors the relevant [go.klarlabs.de/mnemos/internal/domain.Belief] fields
// without importing domain, so the engine stays a leaf; the caller adapts a
// Belief to this shape.
type FloatInput struct {
	ID   string
	Text string
	Type string
	// Active is whether the claim status is "active" — only active claims float.
	Active bool
	// Confidence is the claim's own confidence, carried through so the write
	// side can persist the floated claim with the same confidence.
	Confidence float64
	// Trust is the derived trust_score (confidence × corroboration × freshness).
	Trust float64
	// ConfidenceComponents carries the explicit-global tag (FloatGlobalComponent).
	ConfidenceComponents map[string]float64
}

// FloatItem is one line of the float-back plan.
type FloatItem struct {
	ClaimID    string  `json:"claim_id"`
	Statement  string  `json:"statement"`          // the (path-stripped) text that would be written
	Original   string  `json:"original,omitempty"` // the source text, only when stripping changed it
	Type       string  `json:"type"`
	Trust      float64 `json:"trust"`
	Confidence float64 `json:"confidence"`
	Global     bool    `json:"global"`
	Reason     string  `json:"reason"`
}

// FloatPlan is the auditable output of a selection pass. Every input claim lands
// in exactly one of Floated or Skipped.
type FloatPlan struct {
	Floated []FloatItem `json:"floated"`
	Skipped []FloatItem `json:"skipped"`
}

// PlanFloatBack selects which source claims float up into the central brain.
//
// Gates, in order (first match wins):
//
//  0. active — only active claims float; contested/deprecated are skipped.
//  1. non-empty — a statement that is nothing but a path (empty after strip) is
//     skipped.
//  2. dedup — a statement whose normalized, path-stripped form is already
//     present centrally (existingCentral) or was already floated in this pass is
//     skipped, so re-running never duplicates.
//  3. explicit global — a claim tagged FloatGlobalComponent floats
//     unconditionally (bypassing trust + generality).
//  4. trust — a claim below minTrust is skipped.
//  5. generality — a claim whose statement is repo-specific (bound to a path,
//     filename, or commit sha) is skipped.
//
// Any claim clearing 3, or clearing both 4 and 5, floats. existingCentral is a
// set of already-normalized central statements (see NormalizeForDedup) —
// callers build it from the central brain's current claims.
func PlanFloatBack(inputs []FloatInput, minTrust float64, existingCentral map[string]struct{}) FloatPlan {
	if minTrust <= 0 {
		minTrust = DefaultMinTrust
	}
	seen := make(map[string]struct{}, len(existingCentral))
	for k := range existingCentral {
		seen[k] = struct{}{}
	}

	var plan FloatPlan
	for _, in := range inputs {
		stripped := StripRepoSpecifics(in.Text)
		item := FloatItem{
			ClaimID:    in.ID,
			Statement:  stripped,
			Type:       in.Type,
			Trust:      in.Trust,
			Confidence: in.Confidence,
			Global:     isExplicitGlobal(in.ConfidenceComponents),
		}
		if stripped != strings.TrimSpace(in.Text) {
			item.Original = in.Text
		}
		skip := func(reason string) {
			item.Reason = reason
			plan.Skipped = append(plan.Skipped, item)
		}

		// Gate 0: active only.
		if !in.Active {
			skip(ReasonSkipInactive)
			continue
		}

		// Gate 1: non-empty after strip.
		key := NormalizeForDedup(stripped)
		if key == "" {
			skip(ReasonSkipEmpty)
			continue
		}

		// Gate 2: dedup (central + within this pass).
		if _, dup := seen[key]; dup {
			skip(ReasonSkipDuplicate)
			continue
		}

		// Gate 3: explicit "remember globally" bypasses trust + generality.
		if item.Global {
			item.Reason = ReasonFloatExplicitGlobal
			seen[key] = struct{}{}
			plan.Floated = append(plan.Floated, item)
			continue
		}

		// Gate 4: trust floor.
		if in.Trust < minTrust {
			skip(ReasonSkipBelowTrust)
			continue
		}

		// Gate 5: generality — drop repo-local learnings.
		if IsRepoLocal(in.Text) {
			skip(ReasonSkipRepoLocal)
			continue
		}

		item.Reason = ReasonFloatGeneral
		seen[key] = struct{}{}
		plan.Floated = append(plan.Floated, item)
	}
	return plan
}

func isExplicitGlobal(components map[string]float64) bool {
	v, ok := components[FloatGlobalComponent]
	return ok && v > 0
}

// IsRepoLocal reports whether a statement is tied to this specific repository —
// bound to a filesystem path, a source filename, or a commit sha — rather than
// being a generally-applicable learning. Such statements are skipped by the
// generality gate (unless explicitly tagged global).
func IsRepoLocal(statement string) bool {
	for _, raw := range strings.Fields(statement) {
		w := trimToken(raw)
		if isPathToken(w) || isFileNameToken(w) || isGitSHAToken(w) {
			return true
		}
	}
	return false
}

// StripRepoSpecifics removes obvious repo-specific tokens (absolute/relative
// filesystem paths) from a statement so what lands in the central brain is the
// portable learning, not a machine-local path. It is conservative: it drops
// whole path tokens and collapses the surrounding whitespace, and it never
// touches URLs (a token containing "://"). Filenames and shas are left intact
// here — a statement carrying those is caught by the generality gate instead.
func StripRepoSpecifics(statement string) string {
	fields := strings.Fields(statement)
	out := fields[:0]
	for _, raw := range fields {
		if isPathToken(trimToken(raw)) {
			continue
		}
		out = append(out, raw)
	}
	return strings.Join(out, " ")
}

// NormalizeForDedup reduces a statement to a content-addressed key: lowercased,
// with every run of non-alphanumeric characters collapsed to a single space and
// the ends trimmed. Two statements differing only in punctuation/spacing/case
// map to the same key, so float-back is idempotent across re-runs.
func NormalizeForDedup(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // leading trim
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevSpace = false
			continue
		}
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// trimToken strips surrounding punctuation that a word may carry in prose (a
// trailing period, wrapping quotes/parens) so the token classifiers see the
// bare value.
func trimToken(w string) string {
	return strings.Trim(w, ".,;:!?()[]{}\"'`")
}

// isPathToken reports whether a bare token looks like a filesystem path: a
// Windows drive path, or a POSIX absolute/relative path with at least one
// separator. URLs (containing "://") are explicitly excluded.
func isPathToken(w string) bool {
	if w == "" || strings.Contains(w, "://") {
		return false
	}
	// Windows drive path, e.g. C:\Users\… or C:/Users/…
	if len(w) >= 3 && w[1] == ':' && (w[2] == '\\' || w[2] == '/') {
		return true
	}
	switch {
	case strings.HasPrefix(w, "./"), strings.HasPrefix(w, "../"):
		return true
	case strings.HasPrefix(w, "/"):
		// Require a second separator so a lone "/" or "/word" (often just a
		// slash in prose) is not treated as a path.
		return strings.Count(w, "/") >= 2
	}
	return false
}

// codeExts is the set of file extensions that mark a token as a source/config
// filename — a strong repo-specific signal.
var codeExts = map[string]struct{}{
	"go": {}, "ts": {}, "tsx": {}, "js": {}, "jsx": {}, "py": {}, "rs": {},
	"java": {}, "rb": {}, "php": {}, "c": {}, "cc": {}, "cpp": {}, "h": {},
	"hpp": {}, "cs": {}, "kt": {}, "swift": {}, "scala": {}, "md": {},
	"yaml": {}, "yml": {}, "json": {}, "toml": {}, "sql": {}, "sh": {},
	"env": {}, "cfg": {}, "ini": {}, "lock": {}, "mod": {}, "sum": {},
	"proto": {}, "gradle": {}, "dockerfile": {},
}

// isFileNameToken reports whether a bare token is a filename with a recognised
// source/config extension (e.g. "server.go", "config.yaml"). URLs are excluded.
func isFileNameToken(w string) bool {
	if w == "" || strings.Contains(w, "://") {
		return false
	}
	dot := strings.LastIndexByte(w, '.')
	if dot <= 0 || dot == len(w)-1 {
		return false
	}
	_, ok := codeExts[strings.ToLower(w[dot+1:])]
	return ok
}

// isGitSHAToken reports whether a bare token looks like a git commit sha: 7–40
// hex characters including at least one digit (the digit requirement avoids
// flagging ordinary lowercase words that happen to use only a–f letters).
func isGitSHAToken(w string) bool {
	if len(w) < 7 || len(w) > 40 {
		return false
	}
	hasDigit := false
	for i := 0; i < len(w); i++ {
		c := w[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return hasDigit
}
