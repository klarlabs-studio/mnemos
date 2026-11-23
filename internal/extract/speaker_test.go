package extract

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// extractTexts runs the rule-based engine over one event and returns the claim
// texts in extraction order.
func extractTexts(t *testing.T, content string) []string {
	t.Helper()
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC) },
		nextID: seqClaimIDs(),
	}
	claims, _, err := engine.Extract(context.Background(), []domain.Event{{ID: "ev_1", Content: content}})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Text)
	}
	return out
}

// TestEngineExtract_AttributesLabelledSpeakers pins #445 item 4 on the
// rule-based path: a first- or second-person statement on a line that names
// its speaker becomes a claim that names the speaker (and, in a two-person
// conversation, the addressee), so it stands alone once detached from the
// conversation.
func TestEngineExtract_AttributesLabelledSpeakers(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string // claims that must be present
		notWant []string // claims that must be absent
	}{
		{
			name: "LoCoMo two-speaker session resolves I, my and you",
			content: "Session date: 8 May 2023\n" +
				"Caroline: Sitting in that spot by the window gives me peace.\n" +
				"Melanie: I love making this dairy-free chocolate cake with raspberries.\n" +
				"Caroline: My sister moved to Boston last spring.\n" +
				"Melanie: You told me about the adoption agency last week.",
			want: []string{
				"Sitting in that spot by the window gives Caroline peace",
				"Melanie loves making this dairy-free chocolate cake with raspberries",
				"Caroline's sister moved to Boston last spring",
				"Caroline told Melanie about the adoption agency last week",
			},
			notWant: []string{
				"Caroline: Sitting in that spot by the window gives me peace",
				"I love making this dairy-free chocolate cake with raspberries",
			},
		},
		{
			name: "contractions and auxiliaries agree with the named speaker",
			content: "Caroline: I'm researching adoption agencies right now.\n" +
				"Melanie: I've been painting a lot lately.\n" +
				"Caroline: I don't eat dairy anymore.\n" +
				"Melanie: I'll run the charity race in June.",
			want: []string{
				"Caroline is researching adoption agencies right now",
				"Melanie has been painting a lot lately",
				"Caroline doesn't eat dairy anymore",
				"Melanie will run the charity race in June",
			},
		},
		{
			name: "a past-tense verb and an adverb are left alone",
			content: "Caroline: I went to a LGBTQ support group yesterday.\n" +
				"Melanie: I really enjoy pottery classes on weekends.",
			want: []string{
				"Caroline went to a LGBTQ support group yesterday",
				"Melanie really enjoys pottery classes on weekends",
			},
		},
		{
			name: "a multi-line message keeps its speaker on continuation lines",
			content: "user: I prefer tabs over spaces in Go files.\n" +
				"Also my editor is Neovim.\n" +
				"assistant: I will format the parser with gofmt.",
			want: []string{
				"The user prefers tabs over spaces in Go files",
				"Also the user's editor is Neovim",
				"The assistant will format the parser with gofmt",
			},
		},
		{
			name: "role speakers resolve you to the other role",
			content: "user: Can you check the migration order?\n" +
				"assistant: You should run the schema migration before the backfill.",
			want: []string{
				"The user should run the schema migration before the backfill",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTexts(t, tc.content)
			for _, w := range tc.want {
				if !slices.Contains(got, w) {
					t.Errorf("missing claim %q\n got: %q", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if slices.Contains(got, nw) {
					t.Errorf("unexpected claim %q\n got: %q", nw, got)
				}
			}
		})
	}
}

// TestEngineExtract_AssistantSelfStatementsNeverBecomeUserFacts is the issue's
// rule: what the assistant said about itself is about the assistant.
func TestEngineExtract_AssistantSelfStatementsNeverBecomeUserFacts(t *testing.T) {
	content := "user: Please fix the flaky retry test.\n" +
		"assistant: I found the race in the retry loop.\n" +
		"I prefer table-driven tests for this package.\n" +
		"My next step is adding a deadline to the client."
	got := extractTexts(t, content)
	for _, c := range got {
		lower := strings.ToLower(c)
		if strings.Contains(lower, "the user found") || strings.Contains(lower, "the user prefers") ||
			strings.Contains(lower, "the user's next step") {
			t.Errorf("assistant self-statement attributed to the user: %q", c)
		}
		if strings.HasPrefix(c, "I ") || strings.HasPrefix(c, "My ") {
			t.Errorf("first-person claim left unattributed in a labelled transcript: %q", c)
		}
	}
	for _, w := range []string{
		"The assistant found the race in the retry loop",
		"The assistant prefers table-driven tests for this package",
		"The assistant's next step is adding a deadline to the client",
	} {
		if !slices.Contains(got, w) {
			t.Errorf("missing claim %q\n got: %q", w, got)
		}
	}
}

// TestEngineExtract_NoSpeakerIsInvented pins the other half: text without a
// speaker label keeps its own subject, and labels that are metadata rather than
// speakers are not read as people.
func TestEngineExtract_NoSpeakerIsInvented(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "unlabelled first person is unchanged",
			content: "I love making this dairy-free chocolate cake with raspberries.",
			want:    []string{"I love making this dairy-free chocolate cake with raspberries"},
		},
		{
			name:    "a single name label is not a conversation",
			content: "Caroline: I love making this dairy-free chocolate cake with raspberries.",
			want:    []string{"Caroline: I love making this dairy-free chocolate cake with raspberries"},
		},
		{
			name: "repeated metadata labels are not speakers",
			content: "Note: I think the cache key must include the prompt version.\n" +
				"Note: I measured a 40ms regression on the hot path.",
			want: []string{
				"Note: I think the cache key must include the prompt version",
				"Note: I measured a 40ms regression on the hot path",
			},
		},
		{
			name: "you with no known addressee is left alone",
			content: "Alice: You should rotate the signing key every quarter.\n" +
				"Bob: I agree with the rotation schedule.\n" +
				"Carol: I will draft the runbook for the rotation.",
			want: []string{
				"You should rotate the signing key every quarter",
				"Bob agrees with the rotation schedule",
				"Carol will draft the runbook for the rotation",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTexts(t, tc.content)
			for _, w := range tc.want {
				if !slices.Contains(got, w) {
					t.Errorf("missing claim %q\n got: %q", w, got)
				}
			}
		})
	}
}

func TestAttributeSpeaker(t *testing.T) {
	tests := []struct {
		name      string
		sentence  string
		speaker   string
		addressee string
		want      string
	}{
		{"subject I conjugates", "I love hiking", "Caroline", "", "Caroline loves hiking"},
		{"es ending", "I watch documentaries", "Caroline", "", "Caroline watches documentaries"},
		{"y ending", "I study biology", "Caroline", "", "Caroline studies biology"},
		{"vowel y ending", "I play guitar", "Caroline", "", "Caroline plays guitar"},
		{"am", "I am a counselor", "Caroline", "", "Caroline is a counselor"},
		{"have", "I have two kids", "Melanie", "", "Melanie has two kids"},
		{"was unchanged", "I was nervous", "Melanie", "", "Melanie was nervous"},
		{"modal unchanged", "I can swim", "Melanie", "", "Melanie can swim"},
		{"past ed unchanged", "I painted a sunset", "Melanie", "", "Melanie painted a sunset"},
		{"mid-sentence I", "Yesterday I went camping", "Melanie", "", "Yesterday Melanie went camping"},
		{"compound subject keeps plural verb", "My husband and I love camping", "Melanie", "", "Melanie's husband and Melanie love camping"},
		{"object me", "That book really helped me", "Caroline", "", "That book really helped Caroline"},
		{"mine", "The blue car is mine", "Caroline", "", "The blue car is Caroline's"},
		{"myself", "I taught myself piano", "Caroline", "", "Caroline taught Caroline piano"},
		{"you subject", "You are so talented", "Caroline", "Melanie", "Melanie is so talented"},
		{"you object", "I miss you", "Caroline", "Melanie", "Caroline misses Melanie"},
		{"your", "Your kids are lovely", "Caroline", "Melanie", "Melanie's kids are lovely"},
		{"inverted question aux", "Have you tried pottery", "Caroline", "Melanie", "Has Melanie tried pottery"},
		{"you without addressee", "You should try pottery", "Caroline", "", "You should try pottery"},
		{"role speaker at start", "I prefer tabs", "user", "assistant", "The user prefers tabs"},
		{"role speaker mid-sentence", "That helped me", "user", "assistant", "That helped the user"},
		{"role possessive", "My editor is Neovim", "assistant", "user", "The assistant's editor is Neovim"},
		{"no pronoun unchanged", "The park opens at nine", "Caroline", "Melanie", "The park opens at nine"},
		{"we is left alone", "We went camping", "Melanie", "Caroline", "We went camping"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := attributeSpeaker(tc.sentence, tc.speaker, tc.addressee); got != tc.want {
				t.Errorf("attributeSpeaker(%q, %q, %q) = %q, want %q", tc.sentence, tc.speaker, tc.addressee, got, tc.want)
			}
		})
	}
}
