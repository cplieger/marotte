package agent

// The Memories tab's Import and Export: KAS's _kiro/memory/{getImportPrompt,import,export}, relayed sessionless.
// kiro-cli gates all three on its memory-import experiment and advertises no capability for it, so the tab learns
// availability from getImportPrompt, which runs only that gate and reads no store.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/cplieger/go/httpserver"
	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/marotte/internal/httpreply"
)

// memoryImportMaxBytes is kiro-cli 3.2.1's own import limit (acp-server.js requireImportContent, 10 MiB of UTF-8),
// so an oversize paste is refused here rather than after crossing the wire.
const memoryImportMaxBytes = 10 << 20

// memoryImportComingSoonCode marks KAS's MemoryImportComingSoonError: import and export are not served for this
// account yet.
const memoryImportComingSoonCode = "coming_soon"

const memoryImportComingSoonClass = "MemoryImportComingSoonError"

// maxMemoryImportTextBytes bounds each per-line reason and detail relayed to the client.
const maxMemoryImportTextBytes = 256

// memoryImportLine is one skipped or failed import line. Reason is KAS's open vocabulary (duplicate_in_file,
// quota, validation, secret_blocked, parse_error).
type memoryImportLine struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	Line   int    `json:"line"`
}

// memoryImportReport counts the added and duplicate lines, which the refetched list shows or which need nothing,
// and keeps skipped and failed per line, which are what a user fixes and imports again.
type memoryImportReport struct {
	Skipped    []memoryImportLine `json:"skipped"`
	Failed     []memoryImportLine `json:"failed"`
	TotalLines int                `json:"total_lines"`
	Added      int                `json:"added"`
	Duplicates int                `json:"duplicates"`
}

type memoryImportPromptResponse struct {
	Prompt string `json:"prompt"`
}

func decodeMemoryImportReport(raw json.RawMessage) (memoryImportReport, error) {
	var res struct {
		Added      []json.RawMessage  `json:"added"`
		Duplicates []json.RawMessage  `json:"duplicates"`
		Skipped    []memoryImportLine `json:"skipped"`
		Failed     []memoryImportLine `json:"failed"`
		TotalLines int                `json:"totalLines"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return memoryImportReport{}, err
	}
	return memoryImportReport{
		Skipped: boundedImportLines(res.Skipped), Failed: boundedImportLines(res.Failed),
		TotalLines: res.TotalLines, Added: len(res.Added), Duplicates: len(res.Duplicates),
	}, nil
}

func boundedImportLines(lines []memoryImportLine) []memoryImportLine {
	out := make([]memoryImportLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, memoryImportLine{
			Reason: textsafe.SanitizeSingleLineBounded(l.Reason, maxMemoryImportTextBytes),
			Detail: textsafe.SanitizeSingleLineBounded(l.Detail, maxMemoryImportTextBytes),
			Line:   l.Line,
		})
	}
	return out
}

// handleMemoryImportPrompt serves GET /api/memory/import-prompt: the prompt to give another assistant, or the gate's
// refusal (409 coming_soon or not_enabled), which is how the tab knows whether to offer Import and Export.
func (st *Settings) handleMemoryImportPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	raw, err := st.memoryCall(r.Context(), methodKiroMemoryGetImportPrompt, map[string]any{})
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	var res memoryImportPromptResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		writeMemoryErr(w, err)
		return
	}
	httpserver.WriteJSON(w, res)
}

// handleMemoryImport serves POST /api/memory/import: the raw body is the JSONL KAS imports, and the answer is a
// memoryImportReport.
func (st *Settings) handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpreply.MethodNotAllowed(w, http.MethodPost)
		return
	}
	httpserver.LimitBody(w, r, memoryImportMaxBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			httpserver.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				httpreply.ErrorJSON("The import is over kiro-cli's 10 MiB limit."))
			return
		}
		httpreply.BadRequest(w, "couldn't read the import")
		return
	}
	content := string(body)
	st.memoryWrite(w, r, "memory import", func(ctx context.Context) engineReply {
		raw, err := st.memoryCall(ctx, methodKiroMemoryImport, map[string]any{"content": content})
		if err != nil {
			return memoryFailure(err)
		}
		report, err := decodeMemoryImportReport(raw)
		if err != nil {
			// KAS answered, so the import may have landed; only its report is unreadable.
			return engineReply{err: err, write: func(w http.ResponseWriter) { writeMemoryErr(w, err) }, applied: true}
		}
		return engineReply{write: func(w http.ResponseWriter) { httpserver.WriteJSON(w, report) }, applied: true}
	})
}

// handleMemoryExport serves GET /api/memory/export: KAS's kiro-memory document as a download. KAS's content is
// already the document text, so it is written as is.
func (st *Settings) handleMemoryExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	raw, err := st.memoryCall(r.Context(), methodKiroMemoryExport, map[string]any{})
	if err != nil {
		writeMemoryErr(w, err)
		return
	}
	var res struct {
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		writeMemoryErr(w, err)
		return
	}
	if res.Content == nil {
		writeMemoryErr(w, errors.New("memory export: reply carried no content"))
		return
	}
	w.Header().Set("Content-Disposition",
		`attachment; filename="kiro-memories-`+time.Now().Format(time.DateOnly)+`.json"`)
	httpreply.WriteRawJSON(w, []byte(*res.Content))
}
