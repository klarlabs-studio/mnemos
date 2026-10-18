package mnemos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

const (
	// defaultPassageLimit is how many passages Passages returns when the
	// query names no limit.
	defaultPassageLimit = 10
	// passageCandidateEpisodes is how many episodes the index pre-selects
	// before their passages are ranked. An episode is often a whole
	// conversation session, so a handful hold the passages that matter.
	passageCandidateEpisodes = 20
	// passageLines is how many consecutive non-blank lines make a passage:
	// a turn and its reply.
	passageLines = 2
	// BM25 parameters for ranking passages, the usual Okapi defaults.
	passageK1 = 1.2
	passageB  = 0.75
)

// Passages implements [Memory.Passages]. The candidate episodes (see
// passageCandidates) are cut into passages of passageLines lines, which are
// ranked by BM25 against the query.
func (m *memory) Passages(ctx context.Context, q PassageQuery) ([]Passage, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, errors.New("mnemos: Passages: Text is required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultPassageLimit
	}
	episodes, err := m.passageCandidates(ctx, text)
	if err != nil {
		return nil, err
	}
	out := rankPassages(episodes, text)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// scoredPassage is a passage with the term counts BM25 needs.
type scoredPassage struct {
	passage Passage
	terms   map[string]int
	length  int
}

// rankPassages cuts the episodes into passages and returns those sharing a
// content word with the query, best first. Document frequencies are taken over
// these passages, so a word common in the candidates counts for little.
func rankPassages(episodes []domain.Event, query string) []Passage {
	var cands []scoredPassage
	total := 0
	df := map[string]int{}
	for _, ep := range episodes {
		for _, p := range splitPassages(ep.Content) {
			terms, n := passageTerms(p)
			cands = append(cands, scoredPassage{
				passage: Passage{EpisodeID: ep.ID, Text: p, At: ep.Timestamp, Metadata: ep.Metadata},
				terms:   terms, length: n,
			})
			total += n
			for t := range terms {
				df[t]++
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	want, _ := passageTerms(query)
	n, avg := float64(len(cands)), float64(total)/float64(len(cands))
	var out []Passage
	for _, c := range cands {
		score := 0.0
		for t := range want {
			if f := float64(c.terms[t]); f > 0 {
				idf := math.Log(1 + (n-float64(df[t])+0.5)/(float64(df[t])+0.5))
				score += idf * f * (passageK1 + 1) / (f + passageK1*(1-passageB+passageB*float64(c.length)/avg))
			}
		}
		if score > 0 {
			c.passage.Score = score
			out = append(out, c.passage)
		}
	}
	// Stable, so equal scores keep an episode's passages in line order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// passageCandidates returns the episodes whose passages are ranked: the index's
// best matches, or every episode on a backend without an episode text index
// (the in-memory store), where ranking them all is the same answer.
func (m *memory) passageCandidates(ctx context.Context, text string) ([]domain.Event, error) {
	searcher, ok := m.conn.Events.(ports.TextSearcher)
	if !ok {
		all, err := m.conn.Events.ListAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("mnemos: Passages: list episodes: %w", err)
		}
		return all, nil
	}
	hits, err := searcher.SearchByText(ctx, text, passageCandidateEpisodes)
	if err != nil {
		return nil, fmt.Errorf("mnemos: Passages: search episodes: %w", err)
	}
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	episodes, err := m.conn.Events.ListByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("mnemos: Passages: list episodes: %w", err)
	}
	return episodes, nil
}

// splitPassages cuts an episode into passages of passageLines non-blank lines.
func splitPassages(content string) []string {
	var lines []string
	for _, l := range strings.Split(content, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	var out []string
	for i := 0; i < len(lines); i += passageLines {
		end := min(i+passageLines, len(lines))
		out = append(out, strings.Join(lines[i:end], "\n"))
	}
	return out
}

// passageTerms counts a text's content words under the excerpt rules (three
// or more characters, no stopwords) and returns its length in words.
func passageTerms(s string) (map[string]int, int) {
	out := map[string]int{}
	n := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		n++
		if utf8.RuneCountInString(w) >= 3 && !excerptStopwords[w] {
			out[w]++
		}
	}
	return out, n
}
