package translate

import (
	"encoding/json"
	"testing"
)

// TestACPKiroBlock_SourceIsNestedUnderMetaKiro pins that `source` is a member of
// `_meta.kiro`, not of the update object (KAS's replay builder), in both directions:
// re-nesting it reads "" for every frame.
func TestACPKiroBlock_SourceIsNestedUnderMetaKiro(t *testing.T) {
	t.Run("_meta.kiro.source lands on the field", func(t *testing.T) {
		var u struct {
			Meta ACPKiroMeta `json:"_meta"`
		}
		raw := []byte(`{"sessionUpdate":"user_message_chunk",
			"content":{"type":"text","text":"use tabs"},
			"_meta":{"kiro":{"replay":true,"messageId":"steer-m-1","source":"steer"}}}`)
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if u.Meta.Kiro.Source != "steer" {
			t.Errorf("Source = %q, want %q", u.Meta.Kiro.Source, "steer")
		}
	})

	t.Run("source on the UPDATE object does not", func(t *testing.T) {
		var u struct {
			Meta ACPKiroMeta `json:"_meta"`
		}
		raw := []byte(`{"sessionUpdate":"user_message_chunk","source":"steer",
			"content":{"type":"text","text":"use tabs"},
			"_meta":{"kiro":{"replay":true,"messageId":"steer-m-1"}}}`)
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if u.Meta.Kiro.Source != "" {
			t.Errorf("Source = %q, want empty — a `source` on the update object is NOT the discriminator", u.Meta.Kiro.Source)
		}
	})
}

// TestACPKiroBlock_UserMessageTagIsNestedUnderMetaKiro pins the same nesting for the prompt
// tag, which the resend rule reads as its positive test.
func TestACPKiroBlock_UserMessageTagIsNestedUnderMetaKiro(t *testing.T) {
	t.Run("_meta.kiro.userMessageTag lands on the field", func(t *testing.T) {
		var u struct {
			Meta ACPKiroMeta `json:"_meta"`
		}
		raw := []byte(`{"sessionUpdate":"user_message_chunk",
			"content":{"type":"text","text":"failed:"},
			"_meta":{"kiro":{"replay":true,"messageId":"35a4027b","userMessageTag":"prompt_746336f7"}}}`)
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if u.Meta.Kiro.UserMessageTag != "prompt_746336f7" {
			t.Errorf("UserMessageTag = %q, want %q", u.Meta.Kiro.UserMessageTag, "prompt_746336f7")
		}
	})

	t.Run("userMessageTag on the UPDATE object does not", func(t *testing.T) {
		var u struct {
			Meta ACPKiroMeta `json:"_meta"`
		}
		raw := []byte(`{"sessionUpdate":"user_message_chunk","userMessageTag":"prompt_746336f7",
			"content":{"type":"text","text":"failed:"},
			"_meta":{"kiro":{"replay":true,"messageId":"35a4027b"}}}`)
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if u.Meta.Kiro.UserMessageTag != "" {
			t.Errorf("UserMessageTag = %q, want empty — a tag on the update object is NOT the discriminator", u.Meta.Kiro.UserMessageTag)
		}
	})
}
