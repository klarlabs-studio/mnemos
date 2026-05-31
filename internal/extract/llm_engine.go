package extract

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/felixgeelhaar/fortify/retry"
	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/llm"
)

// TokenUsage reports the input/output token counts a successful LLM
// call consumed. Plumbed up to capability-evidence sinks so axi-go's
// MaxTokens budget enforcer sees the real spend.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	Model        string
}

// LLMEngine extracts claims using an LLM provider. It falls back to the
// rule-based Engine if the LLM call fails.
type LLMEngine struct {
	client   llm.Client
	fallback Engine
	now      func() time.Time
	nextID   func() (string, error)
	cacheDir string
	onUsage  func(TokenUsage)
}

// NewLLMEngine creates an LLM-powered extraction engine with rule-based
// fallback.
func NewLLMEngine(client llm.Client) LLMEngine {
	return LLMEngine{
		client:   client,
		fallback: NewEngine(),
		now:      time.Now,
		nextID:   newClaimID,
		cacheDir: filepath.Join("data", "cache", "llm-extraction"),
	}
}

// WithUsageSink registers a callback the engine invokes after a
// successful LLM call, reporting the token counts the provider
// returned. Used by the pipeline layer to surface token usage as
// capability evidence so axi-go's MaxTokens budget engages. Nil sink
// (the default) is a no-op.
func (e LLMEngine) WithUsageSink(fn func(TokenUsage)) LLMEngine {
	e.onUsage = fn
	return e
}

// llmClaim is the JSON structure returned by the LLM. Entities is
// optional in the LLM output (older prompt versions emit nothing);
// callers handle a nil/empty slice without complaint.
type llmClaim struct {
	Text       string           `json:"text"`
	Type       string           `json:"type"`
	Confidence float64          `json:"confidence"`
	Entities   []llmClaimEntity `json:"entities,omitempty"`
}

// llmClaimEntity is the per-claim entity tag the v1.4+ prompt emits.
// Mapped to ExtractedEntity (the package's external type) by
// buildClaims so downstream code doesn't depend on JSON shapes.
type llmClaimEntity struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Role string `json:"role"`
}

// ExtractedEntity is the package's externally-visible per-claim
// entity record. Mirrors the LLM tag but bound to the canonical
// claim id so downstream consumers (the pipeline, mostly) can
// materialise entities into the entities/claim_entities tables
// without re-parsing JSON.
type ExtractedEntity struct {
	Name string
	Type string
	Role string
}

// Extract processes events through the LLM to extract claims and
// evidence links. Falls back to rule-based extraction on LLM failure.
// Kept as a thin shim over ExtractWithEntities so callers that don't
// care about entities (rule-based downstream paths, older tests) see
// no behavior change.
func (e LLMEngine) Extract(events []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error) {
	claims, links, _, err := e.ExtractWithEntities(events)
	return claims, links, err
}

// ExtractWithEntities is the v0.9 entity-aware extraction entry
// point. The returned map is keyed by the canonical claim id and
// holds the entities the LLM tagged for that claim. The map may be
// nil when the rule-based fallback fires or when the model's output
// predates the v1.4 prompt — callers should treat absent entries as
// "no entities for this claim", not as an error.
func (e LLMEngine) ExtractWithEntities(events []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, map[string][]ExtractedEntity, error) {
	// Collect non-empty event texts.
	var texts []string
	var sourceEvents []domain.Event
	for _, ev := range events {
		content := strings.TrimSpace(ev.Content)
		if content == "" {
			continue
		}
		texts = append(texts, content)
		sourceEvents = append(sourceEvents, ev)
	}

	if len(texts) == 0 {
		return nil, nil, nil, nil
	}

	// Bound the entire extract operation (including up to 3 retries) by
	// 3x the per-request LLM timeout. This lets MNEMOS_LLM_TIMEOUT govern
	// both per-call HTTP budget and total extraction budget without a
	// second knob: the user picks one timeout and gets enough headroom
	// for retries.
	ctx, cancel := context.WithTimeout(context.Background(), 3*llm.Timeout())
	defer cancel()

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: buildExtractionPrompt(texts)},
	}

	cacheKey := e.cacheKey(texts)
	if rawClaims, ok := e.loadCachedClaims(cacheKey); ok {
		claims, links, ents, err := e.buildClaims(rawClaims, sourceEvents)
		return claims, links, ents, err
	}

	retrier := retry.New[llm.Response](retry.Config{
		MaxAttempts:   3,
		InitialDelay:  200 * time.Millisecond,
		MaxDelay:      time.Second,
		BackoffPolicy: retry.BackoffExponential,
		Jitter:        true,
		Logger:        slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	})

	resp, err := retrier.Execute(ctx, func(ctx context.Context) (llm.Response, error) {
		return e.client.Complete(ctx, messages)
	})
	if err != nil {
		// Fallback to rule-based extraction. Rule-based produces no
		// entities, hence the nil third return.
		c, l, ferr := e.fallback.Extract(events)
		return c, l, nil, ferr
	}

	if e.onUsage != nil && (resp.InputTokens > 0 || resp.OutputTokens > 0) {
		e.onUsage(TokenUsage{
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			Model:        resp.Model,
		})
	}

	rawClaims, err := parseLLMResponse(resp.Content)
	if err != nil {
		c, l, ferr := e.fallback.Extract(events)
		return c, l, nil, ferr
	}

	if len(rawClaims) == 0 {
		e.storeCachedClaims(cacheKey, rawClaims)
		return nil, nil, nil, nil
	}
	if cacheKey != "" {
		e.storeCachedClaims(cacheKey, rawClaims)
	}

	return e.buildClaims(rawClaims, sourceEvents)
}

// Convert LLM output to domain claims plus a per-claim entity map.
// Entities are tagged on rawClaims by the v1.4+ prompt; the empty
// map is a valid result (older models or claims without named
// entities).

func (e LLMEngine) buildClaims(rawClaims []llmClaim, sourceEvents []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, map[string][]ExtractedEntity, error) {
	claims := make([]domain.Claim, 0, len(rawClaims))
	evidence := make([]domain.ClaimEvidence, 0, len(rawClaims))
	entities := make(map[string][]ExtractedEntity)
	seen := map[string]struct{}{}

	for _, rc := range rawClaims {
		text := strings.TrimSpace(rc.Text)
		if text == "" {
			continue
		}

		// Filter conversational pollution (greetings, list-headers,
		// status emojis) that LLMs frequently lift verbatim from chat
		// transcripts as standalone "facts". See junk.go for rules.
		if isJunkClaim(text) {
			continue
		}

		dedupeKey := normalizeForDedupe(text)
		if dedupeKey == "" {
			continue
		}
		if _, ok := seen[dedupeKey]; ok {
			continue
		}
		seen[dedupeKey] = struct{}{}

		claimID, err := e.nextID()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("generate claim id: %w", err)
		}

		claimType := parseLLMClaimType(rc.Type)
		confidence := clamp(rc.Confidence, 0.5, 0.95)

		claim := domain.Claim{
			ID:         claimID,
			Text:       text,
			Type:       claimType,
			Confidence: confidence,
			Status:     domain.ClaimStatusActive,
			CreatedAt:  e.now().UTC(),
		}
		if err := claim.Validate(); err != nil {
			continue // Skip invalid claims from LLM.
		}

		// Link claim to the best-matching source event.
		bestEvent := matchEventForClaim(text, sourceEvents)
		ce := domain.ClaimEvidence{ClaimID: claim.ID, EventID: bestEvent.ID}
		if err := ce.Validate(); err != nil {
			continue
		}

		claims = append(claims, claim)
		evidence = append(evidence, ce)

		// Materialise the LLM's entity tags for this claim. Empty,
		// missing-name, or duplicate entries are dropped silently —
		// LLM output is noisy and we'd rather under-tag than poison
		// the entity store.
		if len(rc.Entities) > 0 {
			seenEnt := make(map[string]struct{}, len(rc.Entities))
			for _, ent := range rc.Entities {
				name := strings.TrimSpace(ent.Name)
				if name == "" {
					continue
				}
				typ := strings.TrimSpace(strings.ToLower(ent.Type))
				if typ == "" {
					typ = "concept"
				}
				role := strings.TrimSpace(strings.ToLower(ent.Role))
				if role == "" {
					role = "mention"
				}
				key := strings.ToLower(name) + "\x00" + typ
				if _, dup := seenEnt[key]; dup {
					continue
				}
				seenEnt[key] = struct{}{}
				entities[claim.ID] = append(entities[claim.ID], ExtractedEntity{
					Name: name,
					Type: typ,
					Role: role,
				})
			}
		}
	}

	// Run contested detection on the final claim set.
	markContestedClaims(claims)

	return claims, evidence, entities, nil
}

func (e LLMEngine) cacheKey(texts []string) string {
	if e.cacheDir == "" {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte(PromptVersion))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER"))))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strings.TrimSpace(os.Getenv("MNEMOS_LLM_MODEL"))))
	for _, text := range texts {
		_, _ = h.Write([]byte("\n"))
		_, _ = h.Write([]byte(text))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (e LLMEngine) loadCachedClaims(key string) ([]llmClaim, bool) {
	if key == "" || e.cacheDir == "" {
		return nil, false
	}
	path := filepath.Join(e.cacheDir, key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var claims []llmClaim
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func (e LLMEngine) storeCachedClaims(key string, claims []llmClaim) {
	if key == "" || e.cacheDir == "" {
		return
	}
	if err := os.MkdirAll(e.cacheDir, 0o750); err != nil {
		return
	}
	data, err := json.Marshal(claims)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(e.cacheDir, key+".json"), data, 0o600)
	// Evict oldest entries once the cap is exceeded. Runs on every
	// store rather than on a goroutine so the cache footprint cannot
	// drift past the cap between sweeps. Cost is one Readdir + a
	// bounded number of os.Remove calls per write — negligible vs an
	// LLM round-trip.
	evictCacheIfOverCap(e.cacheDir, llmCacheMaxBytes())
}

// llmCacheMaxBytes returns the cap from MNEMOS_LLM_CACHE_MAX_BYTES,
// defaulting to 1 GiB. Zero or unparseable values disable the sweep
// (return 0) — operators who want unbounded growth (e.g. local benchmark
// runs that want every cache hit) opt in by setting the var to 0.
func llmCacheMaxBytes() int64 {
	v := os.Getenv("MNEMOS_LLM_CACHE_MAX_BYTES")
	if v == "" {
		return 1 << 30 // 1 GiB
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 1 << 30
	}
	return n
}

func evictCacheIfOverCap(dir string, cap int64) {
	if cap <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type cacheFile struct {
		name string
		size int64
		mod  time.Time
	}
	files := make([]cacheFile, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, cacheFile{name: e.Name(), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	if total <= cap {
		return
	}
	// Sort oldest-first; remove until under cap.
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= cap {
			break
		}
		path := filepath.Join(dir, f.name)
		if err := os.Remove(path); err == nil {
			total -= f.size
		}
	}
}

// ExtractClaims implements ports.ExtractionEngine.
func (e LLMEngine) ExtractClaims(events []domain.Event) ([]domain.Claim, error) {
	claims, _, err := e.Extract(events)
	return claims, err
}

// parseLLMResponse extracts the JSON claim array from the LLM response text.
// Tolerates common local-LLM output quirks:
//   - <think>...</think> reasoning blocks (qwen3, deepseek-r1)
//   - prose preambles before the JSON ("Here are the claims: [...]")
//   - markdown fences (```json ... ```)
//   - trailing prose after the JSON
//
// Strict-JSON-only models (cloud providers) fall through unchanged.
func parseLLMResponse(content string) ([]llmClaim, error) {
	cleaned := sanitizeLLMJSON(content)

	var claims []llmClaim
	if err := json.Unmarshal([]byte(cleaned), &claims); err != nil {
		return nil, fmt.Errorf("parse LLM claim JSON: %w", err)
	}
	return claims, nil
}

// thinkBlockRE matches <think>...</think> blocks emitted by reasoning
// models. Case-insensitive so <THINK>, <Think> etc. all match. The (?s)
// flag makes . span newlines.
var thinkBlockRE = regexp.MustCompile(`(?is)<think>.*?</think>`)

// sanitizeLLMJSON strips reasoning tokens, prose, and markdown fences,
// then extracts the first balanced JSON array or object from the text.
// Returns the cleaned string ready for json.Unmarshal.
func sanitizeLLMJSON(content string) string {
	content = thinkBlockRE.ReplaceAllString(content, "")
	content = strings.TrimSpace(content)

	// Strip ```json or ``` fences if present. We don't require them to be
	// the first/last lines because some models put fences mid-response.
	if idx := strings.Index(content, "```"); idx >= 0 {
		// Trim through the opening fence and an optional language tag.
		rest := content[idx+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		// Trim through the closing fence if present.
		if end := strings.Index(rest, "```"); end >= 0 {
			rest = rest[:end]
		}
		content = strings.TrimSpace(rest)
	}

	// Extract the first balanced JSON value (array or object). Falls back
	// to the trimmed content so callers still see the original parse
	// error if no JSON is present.
	if extracted, ok := extractFirstJSONValue(content); ok {
		return extracted
	}
	return content
}

// extractFirstJSONValue scans s for the first balanced JSON array or
// object and returns it. Tracks string boundaries so braces inside
// strings don't confuse the depth counter.
func extractFirstJSONValue(s string) (string, bool) {
	start := -1
	var open, close byte
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			start, open, close = i, '[', ']'
			break
		}
		if s[i] == '{' {
			start, open, close = i, '{', '}'
			break
		}
	}
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// parseLLMClaimType converts LLM string output to a domain ClaimType.
func parseLLMClaimType(raw string) domain.ClaimType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "decision":
		return domain.ClaimTypeDecision
	case "hypothesis":
		return domain.ClaimTypeHypothesis
	default:
		return domain.ClaimTypeFact
	}
}

// matchEventForClaim finds the event whose content best matches the claim text
// using token overlap. Falls back to the first event if no good match.
func matchEventForClaim(claimText string, events []domain.Event) domain.Event {
	if len(events) == 1 {
		return events[0]
	}

	claimNorm := normalizeForDedupe(claimText)
	best := events[0]
	bestScore := -1

	for _, ev := range events {
		evNorm := normalizeForDedupe(ev.Content)
		score := tokenOverlap(claimNorm, evNorm)
		if score > bestScore {
			bestScore = score
			best = ev
		}
	}

	return best
}
