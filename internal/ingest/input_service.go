package ingest

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

var errEmptyContent = errors.New("input content must not be empty")

// Service handles ingestion of raw input sources into domain inputs.
type Service struct {
	now    func() time.Time
	nextID func() (string, error)
}

// NewService returns a Service with default clock and ID generation.
func NewService() Service {
	return Service{
		now:    time.Now,
		nextID: newID,
	}
}

// IngestFile reads a file at the given path and returns a domain input, its text content, and any error.
func (s Service) IngestFile(path string) (domain.Input, string, error) {
	if strings.TrimSpace(path) == "" {
		return domain.Input{}, "", errors.New("path is required")
	}

	contentBytes, err := os.ReadFile(path)
	if err != nil {
		return domain.Input{}, "", fmt.Errorf("read input file: %w", err)
	}

	content := strings.TrimSpace(string(contentBytes))
	if content == "" {
		return domain.Input{}, "", errEmptyContent
	}

	info, err := os.Stat(path)
	if err != nil {
		return domain.Input{}, "", fmt.Errorf("stat input file: %w", err)
	}

	inputType, format, err := classifyByExtension(path)
	if err != nil {
		return domain.Input{}, "", err
	}

	id, err := s.nextID()
	if err != nil {
		return domain.Input{}, "", fmt.Errorf("generate input id: %w", err)
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return domain.Input{}, "", fmt.Errorf("resolve absolute path: %w", err)
	}

	metadata := map[string]string{
		"source":          "file",
		"source_path":     absPath,
		"file_name":       info.Name(),
		"file_size_bytes": strconv.FormatInt(info.Size(), 10),
		"modified_at":     info.ModTime().UTC().Format(time.RFC3339),
	}

	return domain.Input{
		ID:        id,
		Type:      inputType,
		Format:    format,
		Metadata:  metadata,
		CreatedAt: s.now().UTC(),
	}, content, nil
}

// IngestText creates a domain input from raw text and optional metadata.
func (s Service) IngestText(text string, metadata map[string]string) (domain.Input, string, error) {
	content := strings.TrimSpace(text)
	if content == "" {
		return domain.Input{}, "", errEmptyContent
	}

	id, err := s.nextID()
	if err != nil {
		return domain.Input{}, "", fmt.Errorf("generate input id: %w", err)
	}

	mergedMetadata := map[string]string{
		"source": "raw_text",
	}
	for k, v := range metadata {
		mergedMetadata[k] = v
	}

	return domain.Input{
		ID:        id,
		Type:      domain.InputTypeText,
		Format:    "raw",
		Metadata:  mergedMetadata,
		CreatedAt: s.now().UTC(),
	}, content, nil
}

func classifyByExtension(path string) (domain.InputType, string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".txt":
		return domain.InputTypeText, "txt", nil
	case ".md", ".markdown":
		return domain.InputTypeMD, "md", nil
	case ".json":
		return domain.InputTypeJSON, "json", nil
	case ".csv":
		return domain.InputTypeCSV, "csv", nil
	default:
		if ext == "" {
			return "", "", fmt.Errorf("unsupported input format: missing extension for %q", path)
		}
		return "", "", fmt.Errorf("unsupported input format: %s", ext)
	}
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "in_" + hex.EncodeToString(buf), nil
}
