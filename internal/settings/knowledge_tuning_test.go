package settings

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

// The bounds come from kiro-cli 3.2.0's knowledge rows: indexType fast or best ("accurate" is its legacy alias for
// best), maxFiles at least 1, chunking at least 64 words with an overlap from 0 to below the size.
func TestValidateAgentPatch_KnowledgeTuning(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		raw     string
		wantErr bool
	}{
		{name: "index default", key: KeyKnowledgeIndexType, raw: `""`},
		{name: "index fast", key: KeyKnowledgeIndexType, raw: `"fast"`},
		{name: "index best", key: KeyKnowledgeIndexType, raw: `"best"`},
		{name: "index accurate", key: KeyKnowledgeIndexType, raw: `"accurate"`, wantErr: true},
		{name: "index bool", key: KeyKnowledgeIndexType, raw: `true`, wantErr: true},
		{name: "include none", key: KeyKnowledgeIncludePatterns, raw: `[]`},
		{name: "include globs", key: KeyKnowledgeIncludePatterns, raw: `["**/*.md", "docs/**/*.{txt,rst}"]`},
		{name: "include empty entry", key: KeyKnowledgeIncludePatterns, raw: `["**/*.md", ""]`, wantErr: true},
		{name: "include padded entry", key: KeyKnowledgeIncludePatterns, raw: `["**/*.md "]`, wantErr: true},
		{name: "include string", key: KeyKnowledgeIncludePatterns, raw: `"**/*.md"`, wantErr: true},
		{name: "include null", key: KeyKnowledgeIncludePatterns, raw: `null`, wantErr: true},
		{name: "exclude globs", key: KeyKnowledgeExcludePatterns, raw: `["drafts/**"]`},
		{name: "exclude padded entry", key: KeyKnowledgeExcludePatterns, raw: `[" drafts/**"]`, wantErr: true},
		{name: "max files floor", key: KeyKnowledgeMaxFiles, raw: `1`},
		{name: "max files large", key: KeyKnowledgeMaxFiles, raw: `250000`},
		{name: "max files zero", key: KeyKnowledgeMaxFiles, raw: `0`, wantErr: true},
		{name: "max files fraction", key: KeyKnowledgeMaxFiles, raw: `2.5`, wantErr: true},
		{name: "chunking default", key: KeyKnowledgeChunking, raw: `{"size": 512, "overlap": 128}`},
		{name: "chunking floor", key: KeyKnowledgeChunking, raw: `{"size": 64, "overlap": 63}`},
		{name: "chunking no overlap", key: KeyKnowledgeChunking, raw: `{"size": 300, "overlap": 0}`},
		{name: "chunking under floor", key: KeyKnowledgeChunking, raw: `{"size": 63, "overlap": 0}`, wantErr: true},
		{name: "chunking overlap at size", key: KeyKnowledgeChunking, raw: `{"size": 64, "overlap": 64}`, wantErr: true},
		{name: "chunking negative overlap", key: KeyKnowledgeChunking, raw: `{"size": 64, "overlap": -1}`, wantErr: true},
		{name: "chunking overlap missing", key: KeyKnowledgeChunking, raw: `{"size": 300}`, wantErr: true},
		{name: "chunking size missing", key: KeyKnowledgeChunking, raw: `{"overlap": 0}`, wantErr: true},
		{name: "chunking number", key: KeyKnowledgeChunking, raw: `512`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			patch := map[string]json.RawMessage{tc.key: json.RawMessage(tc.raw)}
			if err := validateAgentPatch(patch); (err != nil) != tc.wantErr {
				t.Errorf("validateAgentPatch(%s: %s) = %v, want error %v", tc.key, tc.raw, err, tc.wantErr)
			}
		})
	}
}

// kiro-cli's own knowledge defaults: no index type stated (best), no pattern lists, 10000 files (resolveMaxFiles'
// 1e4), and the knowledgeChunking row's {size:512,overlap:128}.
func TestEffective_KnowledgeTuningDefaultsAreKiroCLIs(t *testing.T) {
	d := EffectiveDefaults()
	if d.KnowledgeIndexType != "" {
		t.Errorf("EffectiveDefaults().KnowledgeIndexType = %q, want \"\"", d.KnowledgeIndexType)
	}
	if d.KnowledgeIncludePatterns == nil || len(d.KnowledgeIncludePatterns) != 0 {
		t.Errorf("EffectiveDefaults().KnowledgeIncludePatterns = %#v, want an empty non-nil list", d.KnowledgeIncludePatterns)
	}
	if d.KnowledgeExcludePatterns == nil || len(d.KnowledgeExcludePatterns) != 0 {
		t.Errorf("EffectiveDefaults().KnowledgeExcludePatterns = %#v, want an empty non-nil list", d.KnowledgeExcludePatterns)
	}
	if d.KnowledgeMaxFiles != 10000 {
		t.Errorf("EffectiveDefaults().KnowledgeMaxFiles = %d, want 10000", d.KnowledgeMaxFiles)
	}
	if want := (marotte.KnowledgeChunking{Size: 512, Overlap: 128}); d.KnowledgeChunking != want {
		t.Errorf("EffectiveDefaults().KnowledgeChunking = %+v, want %+v", d.KnowledgeChunking, want)
	}
}

// A stored knowledge value KAS would not build with reads as the default and is reported, so the page never shows a
// value the engine is not using.
func TestEffectiveFrom_KnowledgeTuning(t *testing.T) {
	stored := map[string]json.RawMessage{
		KeyKnowledgeIndexType:       json.RawMessage(`"fast"`),
		KeyKnowledgeIncludePatterns: json.RawMessage(`["**/*.md"]`),
		KeyKnowledgeExcludePatterns: json.RawMessage(`["drafts/**", ""]`),
		KeyKnowledgeMaxFiles:        json.RawMessage(`300`),
		KeyKnowledgeChunking:        json.RawMessage(`{"size": 128}`),
	}
	got, rejected := EffectiveFrom(stored)
	if got.KnowledgeIndexType != KnowledgeIndexFast || got.KnowledgeMaxFiles != 300 {
		t.Errorf("EffectiveFrom() index type, max files = %q, %d, want fast, 300", got.KnowledgeIndexType, got.KnowledgeMaxFiles)
	}
	if !reflect.DeepEqual(got.KnowledgeIncludePatterns, []string{"**/*.md"}) {
		t.Errorf("EffectiveFrom().KnowledgeIncludePatterns = %q, want [**/*.md]", got.KnowledgeIncludePatterns)
	}
	if len(got.KnowledgeExcludePatterns) != 0 {
		t.Errorf("EffectiveFrom().KnowledgeExcludePatterns = %q, want the default, empty", got.KnowledgeExcludePatterns)
	}
	if got.KnowledgeChunking != DefaultKnowledgeChunking() {
		t.Errorf("EffectiveFrom().KnowledgeChunking = %+v, want the default", got.KnowledgeChunking)
	}
	if want := []string{KeyKnowledgeChunking, KeyKnowledgeExcludePatterns}; !slices.Equal(rejected, want) {
		t.Errorf("EffectiveFrom() rejected %q, want %q", rejected, want)
	}
}

func TestKnowledgeChunkingIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    map[string]json.RawMessage
		want   marotte.KnowledgeChunking
		wantOK bool
	}{
		{"absent", nil, DefaultKnowledgeChunking(), false},
		{
			"stored",
			map[string]json.RawMessage{KeyKnowledgeChunking: json.RawMessage(`{"size": 200, "overlap": 0}`)},
			marotte.KnowledgeChunking{Size: 200},
			true,
		},
		{
			"half written",
			map[string]json.RawMessage{KeyKnowledgeChunking: json.RawMessage(`{"size": 200}`)},
			DefaultKnowledgeChunking(), false,
		},
		{
			"overlap at size",
			map[string]json.RawMessage{KeyKnowledgeChunking: json.RawMessage(`{"size": 200, "overlap": 200}`)},
			DefaultKnowledgeChunking(), false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := KnowledgeChunkingIn(Document{raw: tc.raw})
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("KnowledgeChunkingIn() = %+v, %v, want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
