package lexquery

import (
	"reflect"
	"testing"
)

func TestTerms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		// A LoCoMo-style question: the function words carry no signal and,
		// joined with AND, made the answer-bearing turn unmatchable.
		{"question drops stopwords", "When did Caroline go to the LGBTQ support group?",
			[]string{"caroline", "go", "lgbtq", "support", "group"}},
		// Punctuation inside a token used to reach FTS5 verbatim and raise
		// a syntax error, which the engine swallowed as "no lexical hits".
		{"punctuation splits", "what is 2.5 mg, really", []string{"2", "5", "mg", "really"}},
		{"syntax characters split terms", `alpha OR beta AND "gamma" NOT delta:eps (zeta*)`,
			[]string{"alpha", "beta", "gamma", "delta", "eps", "zeta"}},
		{"dedupes case-insensitively", "Postgres postgres POSTGRES upgrade", []string{"postgres", "upgrade"}},
		{"keeps unicode letters", "Müller's Hündin", []string{"müller", "hündin"}},
		{"drops single letters but keeps digits", "what's x 7", []string{"7"}},
		// Nothing but function words: keep them rather than search for
		// nothing — BM25's IDF already weights them down.
		{"all stopwords falls back", "what is the", []string{"what", "is", "the"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Terms(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Terms(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestTerms_CapsLongQueries(t *testing.T) {
	long := ""
	for i := 0; i < 3*MaxTerms; i++ {
		long += " word" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if got := len(Terms(long)); got != MaxTerms {
		t.Fatalf("len(Terms(long)) = %d, want %d", got, MaxTerms)
	}
}
