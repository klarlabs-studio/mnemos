package parser

import (
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

const schemaVersionV1 = "v1"

// Normalizer converts raw input content into one or more domain events.
type Normalizer struct {
	now    func() time.Time
	nextID func() (string, error)
}

// NewNormalizer returns a Normalizer with default clock and ID generation.
func NewNormalizer() Normalizer {
	return Normalizer{
		now:    time.Now,
		nextID: newEventID,
	}
}

// Normalize parses the content according to the input type and returns chunked domain events.
func (n Normalizer) Normalize(input domain.Input, content string) ([]domain.Event, error) {
	if strings.TrimSpace(input.ID) == "" {
		return nil, errors.New("input id is required")
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, errors.New("content is required")
	}

	switch input.Type {
	case domain.InputTypeCSV:
		return n.normalizeCSV(input, trimmed)
	case domain.InputTypeJSON:
		return n.normalizeJSON(input, trimmed)
	case domain.InputTypeText, domain.InputTypeMD, domain.InputTypeTranscript:
		return n.normalizeText(input, trimmed)
	default:
		return nil, fmt.Errorf("unsupported input type for normalization: %s", input.Type)
	}
}

func (n Normalizer) normalizeText(input domain.Input, content string) ([]domain.Event, error) {
	parts := splitTextChunks(content)
	events := make([]domain.Event, 0, len(parts))
	now := n.now().UTC()
	for i, part := range parts {
		event, err := n.newEvent(input, part, i, now, map[string]string{"chunk_kind": "text"})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		event, err := n.newEvent(input, content, 0, now, map[string]string{"chunk_kind": "text"})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (n Normalizer) normalizeCSV(input domain.Input, content string) ([]domain.Event, error) {
	r := csv.NewReader(strings.NewReader(content))
	records := make([][]string, 0)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse csv: %w", err)
		}
		records = append(records, rec)
	}

	if len(records) == 0 {
		return nil, errors.New("csv has no records")
	}

	now := n.now().UTC()
	events := make([]domain.Event, 0, len(records))
	headers := records[0]
	for i, rec := range records {
		payload, err := marshalRecord(headers, rec, i == 0)
		if err != nil {
			return nil, err
		}
		kind := "csv_row"
		if i == 0 {
			kind = "csv_header"
		}
		event, err := n.newEvent(input, payload, i, now, map[string]string{"chunk_kind": kind})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, nil
}

func (n Normalizer) normalizeJSON(input domain.Input, content string) ([]domain.Event, error) {
	var raw any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	now := n.now().UTC()
	switch v := raw.(type) {
	case []any:
		events := make([]domain.Event, 0, len(v))
		for i, item := range v {
			payload, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("marshal json array item: %w", err)
			}
			event, err := n.newEvent(input, string(payload), i, now, map[string]string{"chunk_kind": "json_array_item"})
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		if len(events) == 0 {
			return nil, errors.New("json array is empty")
		}
		return events, nil
	default:
		payload, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal json object: %w", err)
		}
		event, err := n.newEvent(input, string(payload), 0, now, map[string]string{"chunk_kind": "json_object"})
		if err != nil {
			return nil, err
		}
		return []domain.Event{event}, nil
	}
}

func (n Normalizer) newEvent(input domain.Input, content string, index int, now time.Time, extra map[string]string) (domain.Event, error) {
	id, err := n.nextID()
	if err != nil {
		return domain.Event{}, fmt.Errorf("generate event id: %w", err)
	}
	meta := map[string]string{
		"chunk_index":     strconv.Itoa(index),
		"normalized_from": string(input.Type),
	}
	for k, v := range input.Metadata {
		meta["input_"+k] = v
	}
	for k, v := range extra {
		meta[k] = v
	}

	return domain.Event{
		ID:            id,
		SchemaVersion: schemaVersionV1,
		Content:       content,
		SourceInputID: input.ID,
		Timestamp:     input.CreatedAt,
		Metadata:      meta,
		IngestedAt:    now,
	}, nil
}

func splitTextChunks(content string) []string {
	blocks := strings.Split(content, "\n\n")
	chunks := make([]string, 0, len(blocks))
	for _, block := range blocks {
		part := strings.TrimSpace(block)
		if part == "" {
			continue
		}
		chunks = append(chunks, part)
	}
	return chunks
}

func marshalRecord(headers, record []string, isHeader bool) (string, error) {
	if isHeader {
		payload, err := json.Marshal(map[string]any{"headers": record})
		if err != nil {
			return "", fmt.Errorf("marshal csv header: %w", err)
		}
		return string(payload), nil
	}

	row := map[string]string{}
	for idx, value := range record {
		key := fmt.Sprintf("col_%d", idx)
		if idx < len(headers) {
			name := strings.TrimSpace(headers[idx])
			if name != "" {
				key = name
			}
		}
		row[key] = value
	}
	payload, err := json.Marshal(map[string]any{"row": row})
	if err != nil {
		return "", fmt.Errorf("marshal csv row: %w", err)
	}
	return string(payload), nil
}

func newEventID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "ev_" + hex.EncodeToString(buf), nil
}
