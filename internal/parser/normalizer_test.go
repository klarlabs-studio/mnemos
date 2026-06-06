package parser

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestNormalizeTextSplitsParagraphs(t *testing.T) {
	n := Normalizer{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
		},
		nextID: sequentialIDs(),
	}

	input := domain.Input{ID: "in_1", Type: domain.InputTypeText, CreatedAt: time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)}
	events, err := n.Normalize(input, "first paragraph\n\nsecond paragraph")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Normalize() len = %d, want 2", len(events))
	}
	if events[0].SchemaVersion != "v1" {
		t.Fatalf("SchemaVersion = %q, want v1", events[0].SchemaVersion)
	}
	if events[0].SourceInputID != "in_1" {
		t.Fatalf("SourceInputID = %q, want in_1", events[0].SourceInputID)
	}
	if events[1].Metadata["chunk_index"] != "1" {
		t.Fatalf("chunk_index = %q, want 1", events[1].Metadata["chunk_index"])
	}
}

func TestNormalizeCSV(t *testing.T) {
	n := Normalizer{now: func() time.Time { return time.Now().UTC() }, nextID: sequentialIDs()}
	input := domain.Input{ID: "in_2", Type: domain.InputTypeCSV, CreatedAt: time.Now().UTC()}

	events, err := n.Normalize(input, "name,value\nrevenue,10")
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Normalize() len = %d, want 2", len(events))
	}
	if events[0].Metadata["chunk_kind"] != "csv_header" {
		t.Fatalf("header chunk_kind = %q, want csv_header", events[0].Metadata["chunk_kind"])
	}
	if !strings.Contains(events[1].Content, "revenue") {
		t.Fatalf("row content = %q, expected revenue", events[1].Content)
	}
}

func TestNormalizeJSON(t *testing.T) {
	n := Normalizer{now: func() time.Time { return time.Now().UTC() }, nextID: sequentialIDs()}
	input := domain.Input{ID: "in_3", Type: domain.InputTypeJSON, CreatedAt: time.Now().UTC()}

	events, err := n.Normalize(input, `[{"claim":"one"},{"claim":"two"}]`)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Normalize() len = %d, want 2", len(events))
	}
	if events[0].Metadata["chunk_kind"] != "json_array_item" {
		t.Fatalf("chunk_kind = %q, want json_array_item", events[0].Metadata["chunk_kind"])
	}
}

func TestNormalizeRejectsUnsupportedType(t *testing.T) {
	n := Normalizer{now: func() time.Time { return time.Now().UTC() }, nextID: sequentialIDs()}
	input := domain.Input{ID: "in_4", Type: "xml", CreatedAt: time.Now().UTC()}

	_, err := n.Normalize(input, "<x />")
	if err == nil {
		t.Fatal("Normalize() expected error for unsupported type")
	}
}

func sequentialIDs() func() (string, error) {
	i := 0
	return func() (string, error) {
		id := "ev_test_" + strconv.Itoa(i)
		i++
		return id, nil
	}
}
