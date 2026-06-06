package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
	"go.klarlabs.de/mnemos/internal/llm"
	"go.klarlabs.de/mnemos/internal/relate"
	"gopkg.in/yaml.v3"
)

type TestCase struct {
	ID                string          `yaml:"id"`
	Description       string          `yaml:"description"`
	Tags              []string        `yaml:"tags"`
	Input             string          `yaml:"input"`
	ExpectedClaims    []ExpectedClaim `yaml:"expected_claims"`
	NotExpectedClaims []string        `yaml:"not_expected_claims"`
	ExpectedCount     *int            `yaml:"expected_count"`
	ExpectedMinCount  *int            `yaml:"expected_min_count"`
}

type ExpectedClaim struct {
	Text          string  `yaml:"text"`
	Type          string  `yaml:"type"`
	Status        string  `yaml:"status"`
	MinConfidence float64 `yaml:"min_confidence"`
	MaxConfidence float64 `yaml:"max_confidence"`
}

type TestFile struct {
	TestCases []TestCase `yaml:"test_cases"`
}

func loadTestFiles(t *testing.T) []TestFile {
	evalDir, err := os.Getwd()
	if err != nil {
		t.Skipf("eval directory not found: %v", err)
	}

	files, err := os.ReadDir(evalDir)
	if err != nil {
		t.Skipf("eval directory not found: %v", err)
	}

	var testFiles []TestFile
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") || f.Name() == "schema.yaml" {
			continue
		}
		path := filepath.Join(evalDir, f.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("Failed to read %s: %v", path, err)
			continue
		}

		var tf TestFile
		if err := yaml.Unmarshal(data, &tf); err != nil {
			t.Logf("Failed to parse %s: %v", path, err)
			continue
		}
		testFiles = append(testFiles, tf)
	}
	return testFiles
}

func toClaimType(s string) domain.ClaimType {
	switch s {
	case "decision":
		return domain.ClaimTypeDecision
	case "hypothesis":
		return domain.ClaimTypeHypothesis
	default:
		return domain.ClaimTypeFact
	}
}

func toClaimStatus(s string) domain.ClaimStatus {
	if s == "contested" {
		return domain.ClaimStatusContested
	}
	return domain.ClaimStatusActive
}

func testCaseToEvent(tc TestCase) domain.Event {
	return domain.Event{
		ID:            "test-event-" + tc.ID,
		SchemaVersion: "1.0",
		Content:       tc.Input,
		Timestamp:     time.Now(),
	}
}

func runTestCase(t *testing.T, tc TestCase, engine extract.Engine) {
	event := testCaseToEvent(tc)
	claims, _, err := engine.Extract([]domain.Event{event})
	if err != nil {
		t.Errorf("%s: extraction failed: %v", tc.ID, err)
		return
	}

	if tc.ExpectedCount != nil && len(claims) != *tc.ExpectedCount {
		t.Errorf("%s: expected %d claims, got %d", tc.ID, *tc.ExpectedCount, len(claims))
	}

	if tc.ExpectedMinCount != nil && len(claims) < *tc.ExpectedMinCount {
		t.Errorf("%s: expected at least %d claims, got %d", tc.ID, *tc.ExpectedMinCount, len(claims))
	}

	for _, expected := range tc.ExpectedClaims {
		found := false
		isRealWorld := containsTag(tc.Tags, "real_world")
		for _, claim := range claims {
			normalizedClaim := normalizeClaimText(claim.Text)
			normalizedExpected := normalizeClaimText(expected.Text)

			// Exact match for unit tests, substring match for real-world tests
			match := normalizedClaim == normalizedExpected
			if isRealWorld && strings.Contains(normalizedClaim, normalizedExpected) {
				match = true
			}

			if match {
				found = true

				if claim.Type != toClaimType(expected.Type) {
					t.Errorf("%s: claim '%s' expected type '%s', got '%s'",
						tc.ID, expected.Text, expected.Type, claim.Type)
				}

				if expected.Status != "" && claim.Status != toClaimStatus(expected.Status) {
					t.Errorf("%s: claim '%s' expected status '%s', got '%s'",
						tc.ID, expected.Text, expected.Status, claim.Status)
				}

				if expected.MinConfidence > 0 && claim.Confidence < expected.MinConfidence {
					t.Errorf("%s: claim '%s' confidence %.2f below minimum %.2f",
						tc.ID, expected.Text, claim.Confidence, expected.MinConfidence)
				}

				if expected.MaxConfidence > 0 && claim.Confidence > expected.MaxConfidence {
					t.Errorf("%s: claim '%s' confidence %.2f above maximum %.2f",
						tc.ID, expected.Text, claim.Confidence, expected.MaxConfidence)
				}
				break
			}
		}
		if !found {
			t.Logf("%s: DEBUG actual claims: %v", tc.ID, claims)
			t.Errorf("%s: expected claim not found: '%s'", tc.ID, expected.Text)
		}
	}
}

func normalizeClaimText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "\"")
	text = strings.TrimSuffix(text, "\"")
	text = strings.TrimSuffix(text, ".")
	text = strings.TrimSuffix(text, "!")
	text = strings.TrimSuffix(text, "?")
	return strings.ToLower(text)
}

func TestClaimTypes(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "claim_types") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func TestDeduplication(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "deduplication") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func TestContestedDetection(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "contested_detection") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func TestConfidenceScoring(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "confidence_scoring") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func TestEdgeCases(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "edge_cases") && !containsTag(tc.Tags, "boundaries") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func TestRealWorld(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "real_world") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func TestAllCases(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()
	passed := 0
	failed := 0

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			// Skip LLM-specific and robustness cases in the strict suite.
			// Robustness cases are evaluated separately with substring matching.
			if containsTag(tc.Tags, "llm") || containsTag(tc.Tags, "robustness") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
				if t.Failed() {
					failed++
				} else {
					passed++
				}
			})
		}
	}

	t.Logf("\n=== EVALUATION SUMMARY ===")
	t.Logf("Passed: %d", passed)
	t.Logf("Failed: %d", failed)
	t.Logf("Total:  %d", passed+failed)
	if passed+failed > 0 {
		t.Logf("Pass Rate: %.1f%%", float64(passed)/float64(passed+failed)*100)
	}
}

// TestLLMExtraction runs LLM-specific eval cases against the LLM-powered engine.
// Skipped unless MNEMOS_LLM_PROVIDER is set (requires real API keys).
func TestLLMExtraction(t *testing.T) {
	provider := os.Getenv("MNEMOS_LLM_PROVIDER")
	if provider == "" {
		t.Skip("MNEMOS_LLM_PROVIDER not set; skipping LLM eval")
	}

	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		t.Skipf("LLM config error: %v", err)
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		t.Skipf("LLM client error: %v", err)
	}

	engine := extract.NewLLMEngine(client)
	testFiles := loadTestFiles(t)
	passed := 0
	failed := 0

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "llm") {
				continue
			}
			t.Run(tc.ID, func(t *testing.T) {
				runLLMTestCase(t, tc, engine)
				if t.Failed() {
					failed++
				} else {
					passed++
				}
			})
		}
	}

	t.Logf("\n=== LLM EVAL SUMMARY ===")
	t.Logf("Provider: %s / %s", cfg.Provider, cfg.Model)
	t.Logf("Passed: %d", passed)
	t.Logf("Failed: %d", failed)
	t.Logf("Total:  %d", passed+failed)
	if passed+failed > 0 {
		t.Logf("Pass Rate: %.1f%%", float64(passed)/float64(passed+failed)*100)
	}
}

// runLLMTestCase validates LLM extraction with looser matching (substring)
// since LLMs rephrase claims rather than extracting verbatim text.
func runLLMTestCase(t *testing.T, tc TestCase, engine extract.LLMEngine) {
	event := testCaseToEvent(tc)
	claims, _, err := engine.Extract([]domain.Event{event})
	if err != nil {
		t.Errorf("%s: extraction failed: %v", tc.ID, err)
		return
	}

	if tc.ExpectedCount != nil && len(claims) != *tc.ExpectedCount {
		t.Errorf("%s: expected %d claims, got %d", tc.ID, *tc.ExpectedCount, len(claims))
	}

	if tc.ExpectedMinCount != nil && len(claims) < *tc.ExpectedMinCount {
		t.Errorf("%s: expected at least %d claims, got %d", tc.ID, *tc.ExpectedMinCount, len(claims))
	}

	for _, expected := range tc.ExpectedClaims {
		found := false
		for _, claim := range claims {
			normalizedClaim := normalizeClaimText(claim.Text)
			normalizedExpected := normalizeClaimText(expected.Text)

			// LLM tests use substring matching since LLMs rephrase.
			if strings.Contains(normalizedClaim, normalizedExpected) || strings.Contains(normalizedExpected, normalizedClaim) {
				found = true

				if expected.Type != "" && claim.Type != toClaimType(expected.Type) {
					t.Logf("%s: claim '%s' type mismatch: want '%s', got '%s'",
						tc.ID, expected.Text, expected.Type, claim.Type)
				}

				if expected.MinConfidence > 0 && claim.Confidence < expected.MinConfidence {
					t.Logf("%s: claim '%s' confidence %.2f below min %.2f",
						tc.ID, expected.Text, claim.Confidence, expected.MinConfidence)
				}
				break
			}
		}
		if !found {
			t.Logf("%s: expected claim not found (LLM may have rephrased): '%s'", tc.ID, expected.Text)
			t.Logf("%s: actual claims:", tc.ID)
			for _, c := range claims {
				t.Logf("  - [%s] %s (%.2f)", c.Type, c.Text, c.Confidence)
			}
			t.Errorf("%s: expected claim not found: '%s'", tc.ID, expected.Text)
		}
	}

	for _, notExpected := range tc.NotExpectedClaims {
		normalizedNot := normalizeClaimText(notExpected)
		for _, claim := range claims {
			if strings.Contains(normalizeClaimText(claim.Text), normalizedNot) {
				t.Errorf("%s: unexpected claim found: '%s' matches '%s'", tc.ID, claim.Text, notExpected)
			}
		}
	}
}

// TestPrecisionRecall computes extraction precision and recall across all
// non-LLM eval cases. Precision = correct extractions / total extractions.
// Recall = found expected claims / total expected claims.
func TestPrecisionRecall(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	type categoryMetrics struct {
		truePositives  int // expected claims found
		falseNegatives int // expected claims NOT found
		totalExtracted int // all claims extracted
	}

	byCategory := map[string]*categoryMetrics{}
	aggregate := &categoryMetrics{}

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if containsTag(tc.Tags, "llm") {
				continue
			}

			event := testCaseToEvent(tc)
			claims, _, err := engine.Extract([]domain.Event{event})
			if err != nil {
				continue
			}

			// Determine primary category from first tag.
			cat := "unknown"
			if len(tc.Tags) > 0 {
				cat = tc.Tags[0]
			}
			if byCategory[cat] == nil {
				byCategory[cat] = &categoryMetrics{}
			}
			m := byCategory[cat]

			m.totalExtracted += len(claims)
			aggregate.totalExtracted += len(claims)

			for _, expected := range tc.ExpectedClaims {
				isRealWorld := containsTag(tc.Tags, "real_world")
				found := false
				for _, claim := range claims {
					nc := normalizeClaimText(claim.Text)
					ne := normalizeClaimText(expected.Text)
					if nc == ne || (isRealWorld && strings.Contains(nc, ne)) {
						found = true
						break
					}
				}
				if found {
					m.truePositives++
					aggregate.truePositives++
				} else {
					m.falseNegatives++
					aggregate.falseNegatives++
				}
			}
		}
	}

	t.Logf("\n=== PRECISION / RECALL REPORT ===")
	t.Logf("%-25s %6s %6s %6s %8s %8s", "Category", "TP", "FN", "Ext", "Prec", "Recall")
	t.Logf("%-25s %6s %6s %6s %8s %8s", "--------", "--", "--", "---", "----", "------")

	for cat, m := range byCategory {
		precision := float64(0)
		if m.totalExtracted > 0 {
			precision = float64(m.truePositives) / float64(m.totalExtracted)
		}
		recall := float64(0)
		totalExpected := m.truePositives + m.falseNegatives
		if totalExpected > 0 {
			recall = float64(m.truePositives) / float64(totalExpected)
		}
		t.Logf("%-25s %6d %6d %6d %7.1f%% %7.1f%%", cat, m.truePositives, m.falseNegatives, m.totalExtracted, precision*100, recall*100)
	}

	// Aggregate.
	precision := float64(0)
	if aggregate.totalExtracted > 0 {
		precision = float64(aggregate.truePositives) / float64(aggregate.totalExtracted)
	}
	totalExpected := aggregate.truePositives + aggregate.falseNegatives
	recall := float64(0)
	if totalExpected > 0 {
		recall = float64(aggregate.truePositives) / float64(totalExpected)
	}
	f1 := float64(0)
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}

	t.Logf("%-25s %6s %6s %6s %8s %8s", "--------", "--", "--", "---", "----", "------")
	t.Logf("%-25s %6d %6d %6d %7.1f%% %7.1f%%", "AGGREGATE", aggregate.truePositives, aggregate.falseNegatives, aggregate.totalExtracted, precision*100, recall*100)
	t.Logf("F1 Score: %.1f%%", f1*100)

	// Reliability floor. Baseline at slice F (2026-04-19): P=81.9%
	// R=94.6% F1=87.8%. We allow a 1pt buffer per metric so a
	// genuinely-improving change with a tiny tradeoff doesn't
	// flake; anything sliding worse than that is a reliability
	// regression and the test fails. If a refactor legitimately
	// moves the floor (up or down with justification), update the
	// constants in the same commit.
	const (
		minPrecision = 0.80
		minRecall    = 0.93
		minF1        = 0.86
	)
	if precision < minPrecision {
		t.Errorf("precision regression: got %.1f%%, baseline floor %.1f%%", precision*100, minPrecision*100)
	}
	if recall < minRecall {
		t.Errorf("recall regression: got %.1f%%, baseline floor %.1f%%", recall*100, minRecall*100)
	}
	if f1 < minF1 {
		t.Errorf("F1 regression: got %.1f%%, baseline floor %.1f%%", f1*100, minF1*100)
	}

	// Per-category F1 floors. Aggregate alone hides the failure mode
	// where one suite collapses (say causal_detection F1 drops 0.95→
	// 0.40) while another lifts the average. Each suite gets its own
	// floor; if a category's name isn't in this map it's treated as
	// "no floor required" so newly-added suites don't fail CI before
	// a baseline lands. Update the floor and bump the date when
	// committing an intentional baseline change.
	//
	// Baseline as of 2026-05-09 (first per-category gate). Floors set
	// 5 percentage points below observed F1 to allow noise on small
	// suites; tighten as the suites stabilise.
	categoryFloors := map[string]float64{
		"claim_types":         0.95, // observed 100%
		"deduplication":       0.95, // observed 100%
		"contested_detection": 0.95, // observed 100%
		"confidence_scoring":  0.90, // observed 100%
		"real_world":          0.85, // observed F1 ≈ 91.6% (P=84.5% R=100%)
		"robustness":          0.45, // observed F1 ≈ 52.7% (noisy adversarial set)
		"edge_cases":          0.95, // observed F1 ≈ 98.7%
	}
	for cat, m := range byCategory {
		floor, ok := categoryFloors[cat]
		if !ok {
			continue
		}
		catTotalExpected := m.truePositives + m.falseNegatives
		catP := 0.0
		if m.totalExtracted > 0 {
			catP = float64(m.truePositives) / float64(m.totalExtracted)
		}
		catR := 0.0
		if catTotalExpected > 0 {
			catR = float64(m.truePositives) / float64(catTotalExpected)
		}
		catF1 := 0.0
		if catP+catR > 0 {
			catF1 = 2 * catP * catR / (catP + catR)
		}
		if catF1 < floor {
			t.Errorf("category %q F1 regression: got %.1f%%, floor %.1f%% (TP=%d FN=%d Ext=%d)",
				cat, catF1*100, floor*100, m.truePositives, m.falseNegatives, m.totalExtracted)
		}
	}
}

// TestRelationshipDetection evaluates the relate engine against annotated
// claim pairs with expected relationship types.
func TestRelationshipDetection(t *testing.T) {
	testFiles := loadRelationshipTestFiles(t)
	relEngine := relate.NewEngine()

	truePositives := 0
	falsePositives := 0
	falseNegatives := 0

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			t.Run(tc.ID, func(t *testing.T) {
				claims := make([]domain.Claim, len(tc.Claims))
				for i, c := range tc.Claims {
					claims[i] = domain.Claim{ID: c.ID, Text: c.Text}
				}

				rels, err := relEngine.Detect(claims)
				if err != nil {
					t.Fatalf("Detect() error = %v", err)
				}

				// Build lookup of actual relationships.
				type edge struct{ from, to string }
				actual := map[edge]domain.RelationshipType{}
				for _, rel := range rels {
					actual[edge{rel.FromClaimID, rel.ToClaimID}] = rel.Type
				}

				// Check expected relationships.
				for _, exp := range tc.ExpectedRelationships {
					e := edge{exp.FromClaimID, exp.ToClaimID}
					got, ok := actual[e]
					if !ok {
						// Try reverse direction.
						e = edge{exp.ToClaimID, exp.FromClaimID}
						got, ok = actual[e]
					}
					if !ok {
						falseNegatives++
						t.Errorf("%s: expected %s relationship between %s and %s, not found",
							tc.ID, exp.Type, exp.FromClaimID, exp.ToClaimID)
					} else if string(got) != exp.Type {
						falsePositives++
						t.Errorf("%s: relationship between %s and %s: want %s, got %s",
							tc.ID, exp.FromClaimID, exp.ToClaimID, exp.Type, got)
					} else {
						truePositives++
					}
				}

				// Check that no unexpected relationships are detected.
				if tc.ExpectedNone && len(rels) > 0 {
					falsePositives += len(rels)
					t.Errorf("%s: expected no relationships, got %d", tc.ID, len(rels))
				}
			})
		}
	}

	total := truePositives + falsePositives + falseNegatives
	t.Logf("\n=== RELATIONSHIP DETECTION EVAL ===")
	t.Logf("True Positives:  %d", truePositives)
	t.Logf("False Positives: %d", falsePositives)
	t.Logf("False Negatives: %d", falseNegatives)
	if truePositives+falsePositives > 0 {
		t.Logf("Precision: %.1f%%", float64(truePositives)/float64(truePositives+falsePositives)*100)
	}
	if truePositives+falseNegatives > 0 {
		t.Logf("Recall:    %.1f%%", float64(truePositives)/float64(truePositives+falseNegatives)*100)
	}
	t.Logf("Total:     %d", total)
}

// Relationship eval types and loader.
type RelationshipTestCase struct {
	ID                    string            `yaml:"id"`
	Description           string            `yaml:"description"`
	Claims                []RelTestClaim    `yaml:"claims"`
	ExpectedRelationships []RelTestExpected `yaml:"expected_relationships"`
	ExpectedNone          bool              `yaml:"expected_none"`
}

type RelTestClaim struct {
	ID        string `yaml:"id"`
	Text      string `yaml:"text"`
	ValidFrom string `yaml:"valid_from"`
}

type RelTestExpected struct {
	FromClaimID string `yaml:"from_claim_id"`
	ToClaimID   string `yaml:"to_claim_id"`
	Type        string `yaml:"type"`
}

type RelationshipTestFile struct {
	TestCases []RelationshipTestCase `yaml:"test_cases"`
}

func loadRelationshipTestFiles(t *testing.T) []RelationshipTestFile {
	evalDir, err := os.Getwd()
	if err != nil {
		t.Skipf("eval directory not found: %v", err)
	}

	path := filepath.Join(evalDir, "relationship_detection.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("relationship_detection.yaml not found: %v", err)
	}

	var tf RelationshipTestFile
	if err := yaml.Unmarshal(data, &tf); err != nil {
		t.Fatalf("Failed to parse relationship_detection.yaml: %v", err)
	}
	return []RelationshipTestFile{tf}
}

// CausalTestCase mirrors RelationshipTestCase for the causal-detection
// eval. ExpectedCausal lists pairs that DetectCausal must emit a
// `causes` edge for; ExpectedNone is a "no causal edges" assertion.
type CausalTestCase struct {
	ID             string            `yaml:"id"`
	Description    string            `yaml:"description"`
	Claims         []RelTestClaim    `yaml:"claims"`
	ExpectedCausal []RelTestExpected `yaml:"expected_causal"`
	ExpectedNone   bool              `yaml:"expected_none"`
}

type CausalTestFile struct {
	TestCases []CausalTestCase `yaml:"test_cases"`
}

func loadCausalTestFiles(t *testing.T) []CausalTestFile {
	evalDir, err := os.Getwd()
	if err != nil {
		t.Skipf("eval directory not found: %v", err)
	}
	path := filepath.Join(evalDir, "causal_detection.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("causal_detection.yaml not found: %v", err)
	}
	var tf CausalTestFile
	if err := yaml.Unmarshal(data, &tf); err != nil {
		t.Fatalf("Failed to parse causal_detection.yaml: %v", err)
	}
	return []CausalTestFile{tf}
}

// TestCausalDetection evaluates the relate engine's DetectCausal
// heuristic against annotated claim pairs. Phase 1 of the
// evidence+causality+outcomes initiative.
func TestCausalDetection(t *testing.T) {
	files := loadCausalTestFiles(t)
	relEngine := relate.NewEngine()

	truePositives := 0
	falsePositives := 0
	falseNegatives := 0

	for _, tf := range files {
		for _, tc := range tf.TestCases {
			t.Run(tc.ID, func(t *testing.T) {
				claims := make([]domain.Claim, len(tc.Claims))
				for i, c := range tc.Claims {
					var vf time.Time
					if c.ValidFrom != "" {
						parsed, err := time.Parse(time.RFC3339, c.ValidFrom)
						if err != nil {
							t.Fatalf("%s: parse valid_from %q: %v", tc.ID, c.ValidFrom, err)
						}
						vf = parsed
					}
					claims[i] = domain.Claim{ID: c.ID, Text: c.Text, ValidFrom: vf}
				}

				rels, err := relEngine.DetectCausal(claims)
				if err != nil {
					t.Fatalf("DetectCausal() error = %v", err)
				}

				type edge struct{ from, to string }
				actual := map[edge]domain.RelationshipType{}
				for _, rel := range rels {
					actual[edge{rel.FromClaimID, rel.ToClaimID}] = rel.Type
				}

				for _, exp := range tc.ExpectedCausal {
					e := edge{exp.FromClaimID, exp.ToClaimID}
					got, ok := actual[e]
					if !ok {
						falseNegatives++
						t.Errorf("%s: expected causes edge %s -> %s, not found",
							tc.ID, exp.FromClaimID, exp.ToClaimID)
					} else if got != domain.RelationshipTypeCauses {
						falsePositives++
						t.Errorf("%s: edge %s -> %s: want causes, got %s",
							tc.ID, exp.FromClaimID, exp.ToClaimID, got)
					} else {
						truePositives++
					}
				}

				if tc.ExpectedNone && len(rels) > 0 {
					falsePositives += len(rels)
					t.Errorf("%s: expected no causal edges, got %d (%v)", tc.ID, len(rels), rels)
				}
			})
		}
	}

	t.Logf("\n=== CAUSAL DETECTION EVAL ===")
	t.Logf("True Positives:  %d", truePositives)
	t.Logf("False Positives: %d", falsePositives)
	t.Logf("False Negatives: %d", falseNegatives)
	if truePositives+falsePositives > 0 {
		t.Logf("Precision: %.1f%%", float64(truePositives)/float64(truePositives+falsePositives)*100)
	}
	if truePositives+falseNegatives > 0 {
		t.Logf("Recall:    %.1f%%", float64(truePositives)/float64(truePositives+falseNegatives)*100)
	}
}

func TestRobustness(t *testing.T) {
	testFiles := loadTestFiles(t)
	engine := extract.NewEngine()

	for _, tf := range testFiles {
		for _, tc := range tf.TestCases {
			if !containsTag(tc.Tags, "robustness") {
				continue
			}
			// Force real_world-style substring matching for robustness tests
			// since rule-based extraction preserves prefix noise.
			if !containsTag(tc.Tags, "real_world") {
				tc.Tags = append(tc.Tags, "real_world")
			}
			t.Run(tc.ID, func(t *testing.T) {
				runTestCase(t, tc, engine)
			})
		}
	}
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
