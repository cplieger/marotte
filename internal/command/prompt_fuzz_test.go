package command

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/marotte/internal/marotte"
)

func FuzzValidatePromptPayload(f *testing.F) {
	f.Add([]byte(`{"text":"hello","message_id":"abc-123","model":"claude"}`))
	f.Add([]byte(`{"text":"","message_id":"abc-123","model":"claude"}`))
	f.Add([]byte(`{"text":"hi","message_id":"","model":"m"}`))
	f.Add([]byte(`{"text":"hi","message_id":"../evil","model":"m"}`))
	f.Add(make([]byte, 600000))
	f.Add([]byte(`{broken`))
	f.Add([]byte(`{"text":"","message_id":"abc-123","attachments":[{"path":"/workspace/a.png"}]}`))
	f.Add([]byte(`{"text":"","message_id":"abc-123","attachments":[{"path":""}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cmd := &marotte.ClientCommand{Payload: json.RawMessage(data)}
		p, code, err := validatePromptPayload(cmd)

		if err == nil && code != 0 {
			t.Errorf("err==nil but code=%d", code)
		}
		if err != nil && code == 0 {
			t.Errorf("err=%v but code=0", err)
		}

		if err == nil {
			if p.Text == "" && len(p.Attachments) == 0 {
				t.Error("validation passed with neither text nor attachments")
			}
			if len(p.Attachments) > marotte.MaxAttachments {
				t.Errorf("validation passed with %d attachments, over the cap", len(p.Attachments))
			}
			for _, a := range p.Attachments {
				if a.Path == "" || len(a.Path) > marotte.MaxAttachmentPathBytes {
					t.Errorf("validation passed with attachment path of %d bytes", len(a.Path))
				}
			}
			if len(p.Text) > MaxPromptBytes {
				t.Error("validation passed but Text exceeds cap")
			}
			if p.MessageID == "" {
				t.Error("validation passed but MessageID is empty")
			}
		}
	})
}
