package settings

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cplieger/marotte/internal/marotte"
)

// KeyKnowledgeIndexType's chosen values, KAS's index types: keyword (BM25) and semantic embeddings. KAS's third,
// "accurate", is a legacy alias for best (normalizeIndexType in 3.2.0), so no setting stores it.
const (
	KnowledgeIndexFast = "fast"
	KnowledgeIndexBest = "best"
)

// kiro-cli's own knowledge defaults in 3.2.0: resolveMaxFiles' 1e4, and the knowledgeChunking row's
// {size:512,overlap:128} with its size floor of 64. A value at its default is withheld.
const (
	DefaultKnowledgeMaxFiles     = 10000
	DefaultKnowledgeChunkSize    = 512
	DefaultKnowledgeChunkOverlap = 128
	minKnowledgeChunkSize        = 64
)

// DefaultKnowledgeChunking is the knowledge_chunking value config.json is silent about.
func DefaultKnowledgeChunking() marotte.KnowledgeChunking {
	return marotte.KnowledgeChunking{Size: DefaultKnowledgeChunkSize, Overlap: DefaultKnowledgeChunkOverlap}
}

// ValidKnowledgeIndexType reports whether v is unset ("") or one of KAS's two index types.
func ValidKnowledgeIndexType(v string) bool {
	return v == "" || v == KnowledgeIndexFast || v == KnowledgeIndexBest
}

// ValidKnowledgeMaxFiles reports whether n is a file cap KAS's knowledgeMaxFiles accepts: at least 1.
func ValidKnowledgeMaxFiles(n int) bool {
	return n >= 1
}

// ValidKnowledgeChunking reports whether c is a chunking KAS builds rather than replacing with its default: a size of
// at least 64 words and an overlap from 0 to below the size.
func ValidKnowledgeChunking(c marotte.KnowledgeChunking) bool {
	return c.Size >= minKnowledgeChunkSize && c.Overlap >= 0 && c.Overlap < c.Size
}

// ValidKnowledgePatterns reports whether every entry is a non-empty glob with no surrounding whitespace.
func ValidKnowledgePatterns(patterns []string) bool {
	for _, p := range patterns {
		if p == "" || strings.TrimSpace(p) != p {
			return false
		}
	}
	return true
}

// decodeKnowledgeChunking is decodeChecked for knowledge_chunking, refusing an object missing either member, so a
// half-written value never reads as an overlap of 0.
func decodeKnowledgeChunking(dst *marotte.KnowledgeChunking, raw json.RawMessage) error {
	var members map[string]json.RawMessage
	if err := decodeInto(&members, raw); err != nil {
		return err
	}
	if _, ok := members["size"]; !ok {
		return fmt.Errorf("settings: %s has no size", KeyKnowledgeChunking)
	}
	if _, ok := members["overlap"]; !ok {
		return fmt.Errorf("settings: %s has no overlap", KeyKnowledgeChunking)
	}
	return decodeChecked(dst, raw, KeyKnowledgeChunking, ValidKnowledgeChunking)
}

// KnowledgeChunkingIn reads knowledge_chunking from d as the effective view does; ok is false for an absent or
// refused value, which reads as DefaultKnowledgeChunking.
func KnowledgeChunkingIn(d Document) (c marotte.KnowledgeChunking, ok bool) {
	raw, present := d.raw[KeyKnowledgeChunking]
	if !present || decodeKnowledgeChunking(&c, raw) != nil {
		return DefaultKnowledgeChunking(), false
	}
	return c, true
}

// knowledgePatchChecks refuse a knowledge value KAS would not build with, each with the sentence a PATCH answers.
var knowledgePatchChecks = map[string]func(json.RawMessage) error{
	KeyKnowledgeIndexType: func(r json.RawMessage) error {
		var v string
		if decodeChecked(&v, r, KeyKnowledgeIndexType, ValidKnowledgeIndexType) != nil {
			return fmt.Errorf(`%s must be "", %q or %q`, KeyKnowledgeIndexType, KnowledgeIndexFast, KnowledgeIndexBest)
		}
		return nil
	},
	KeyKnowledgeIncludePatterns: func(r json.RawMessage) error { return checkKnowledgePatterns(KeyKnowledgeIncludePatterns, r) },
	KeyKnowledgeExcludePatterns: func(r json.RawMessage) error { return checkKnowledgePatterns(KeyKnowledgeExcludePatterns, r) },
	KeyKnowledgeMaxFiles: func(r json.RawMessage) error {
		var v int
		if decodeChecked(&v, r, KeyKnowledgeMaxFiles, ValidKnowledgeMaxFiles) != nil {
			return fmt.Errorf("%s must be a whole number of at least 1", KeyKnowledgeMaxFiles)
		}
		return nil
	},
	KeyKnowledgeChunking: func(r json.RawMessage) error {
		var v marotte.KnowledgeChunking
		if decodeKnowledgeChunking(&v, r) != nil {
			return fmt.Errorf("%s must have a size of at least %d words and an overlap from 0 to below the size",
				KeyKnowledgeChunking, minKnowledgeChunkSize)
		}
		return nil
	},
}

func checkKnowledgePatterns(key string, raw json.RawMessage) error {
	var v []string
	if decodeChecked(&v, raw, key, ValidKnowledgePatterns) != nil {
		return fmt.Errorf("%s must be a list of patterns, none empty or with surrounding spaces", key)
	}
	return nil
}
