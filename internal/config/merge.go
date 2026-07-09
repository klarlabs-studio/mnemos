package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetValues merges dotted-key values into a YAML config file, creating it (and
// its parent directory, mode 0600) when absent. It preserves existing content,
// comments, and unknown keys by editing the document's node tree in place.
//
// Keys are dotted paths into the nested config, e.g.:
//
//	SetValues(path, map[string]string{
//	    "db.url":       "postgres://…",
//	    "llm.provider": "ollama",
//	})
//
// This is how `mnemos init` persists a hosted DSN (with credentials) into a
// 0600 config file instead of inlining it into Claude Code's settings.
func SetValues(path string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}

	var doc yaml.Node
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse %s: %w (fix or move it aside and re-run)", path, err)
		}
	}
	mapping := documentMapping(&doc)

	// Deterministic order so repeated runs produce stable files.
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		setNested(mapping, strings.Split(k, "."), kv[k])
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// documentMapping returns the top-level mapping node of a YAML document,
// initializing an empty document into `document → mapping` when needed.
func documentMapping(doc *yaml.Node) *yaml.Node {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	return doc.Content[0]
}

// mappingChild returns the value node for key in mapping, creating a mapping
// pair when the key is absent.
func mappingChild(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, k, v)
	return v
}

// setNested walks/creates nested mappings for keys[:len-1] and sets the final
// key to a scalar string value.
func setNested(mapping *yaml.Node, keys []string, val string) {
	cur := mapping
	for _, k := range keys[:len(keys)-1] {
		child := mappingChild(cur, k)
		if child.Kind != yaml.MappingNode {
			*child = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		cur = child
	}
	leaf := mappingChild(cur, keys[len(keys)-1])
	*leaf = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val}
}
