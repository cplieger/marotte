package command

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/testsupport"
	"github.com/cplieger/marotte/internal/workspace"
)

// kasSupportedDocumentMIMEs mirrors KAS's SUPPORTED_DOCUMENT_MIME_TYPES (src/document-types.ts in
// the @kiro/agent bundle, KAS 2.12.0).
var kasSupportedDocumentMIMEs = map[string]bool{
	"application/pdf":    true,
	"text/csv":           true,
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"text/html":     true,
	"text/plain":    true,
	"text/markdown": true,
}

// TestDocumentExtsSubsetOfKAS asserts that documentExts must be a subset of KAS's allowlist, or the inlined
// resource is silently dropped.
func TestDocumentExtsSubsetOfKAS(t *testing.T) {
	for ext, mime := range documentExts {
		if !kasSupportedDocumentMIMEs[mime] {
			t.Errorf("documentExts[%q] = %q is not in KAS SUPPORTED_DOCUMENT_MIME_TYPES; "+
				"KAS would silently drop it — remove it or route %s through the path-reference branch",
				ext, mime, ext)
		}
	}
}

// TestUnsupportedDocExtsDisjointFromDocumentExts guards the two attachment
// maps against overlap: an extension inlined as a resource block
// (documentExts) must never also be listed as an unsupported format routed
// to a path reference (unsupportedDocExts), or the intent of one is dead.
func TestUnsupportedDocExtsDisjointFromDocumentExts(t *testing.T) {
	for ext := range unsupportedDocExts {
		if _, ok := documentExts[ext]; ok {
			t.Errorf("extension %q is in both documentExts and unsupportedDocExts", ext)
		}
	}
}

// kasImageExtensions mirrors KAS's IMAGE_EXTENSIONS (verified against the
// 2.18.1 bundle's acp-server.js, beside MAX_IMAGE_SIZE = 10 MiB). It is the set
// KAS's own image read tool accepts, which is what makes the path-reference
// fallback work for an image that cannot be inlined.
var kasImageExtensions = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
}

// TestImageExtsMatchesKAS pins imageExts to KAS's image extension set in BOTH directions, which
// fail differently.
func TestImageExtsMatchesKAS(t *testing.T) {
	for ext := range imageExts {
		if !kasImageExtensions[ext] {
			t.Errorf("imageExts has %q, which KAS's IMAGE_EXTENSIONS does not: "+
				"neither the inline block nor the path-reference fallback can be read", ext)
		}
	}
	for ext := range kasImageExtensions {
		if _, ok := imageExts[ext]; !ok {
			t.Errorf("KAS accepts %q but imageExts omits it, so it degrades to a path "+
				"reference when it could have been shown to the model", ext)
		}
	}
}

// TestImageExtsExcludesUnviewableFormats pins the two exclusions that look like
// oversights. KAS omits both, and an SVG is XML rather than pixels, so inlining
// one hands a vision model markup to look at.
func TestImageExtsExcludesUnviewableFormats(t *testing.T) {
	for _, ext := range []string{".svg", ".avif", ".bmp", ".tiff"} {
		if _, ok := imageExts[ext]; ok {
			t.Errorf("imageExts must not carry %q: KAS does not accept it as an image", ext)
		}
	}
}

// TestAttachmentBlock_ImageInlinesAsImageBlock pins `{type:"image", data:<base64>,
// mimeType:<mime>}` and NO uri; the uri assertion is the load-bearing one (toDataUrl).
func TestAttachmentBlock_ImageInlinesAsImageBlock(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "shot.png")
	pixels := pngBytes(t, 8, 6)
	if err := os.WriteFile(abs, pixels, 0o600); err != nil {
		t.Fatal(err)
	}

	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs, Name: "shot.png"},
		Workspace{Dir: dir}, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != "image" {
		t.Fatalf("block type = %v, want image", got)
	}
	if got := block["mimeType"]; got != "image/png" {
		t.Errorf("mimeType = %v, want image/png", got)
	}
	if got, want := block["data"], base64.StdEncoding.EncodeToString(pixels); got != want {
		t.Errorf("data = %v, want the base64 of the file bytes", got)
	}
	if _, ok := block["uri"]; ok {
		t.Error("block carries a uri; KAS's toDataUrl would return it instead of the bytes")
	}
	if want := base64.StdEncoding.EncodedLen(len(pixels)); spent != want {
		t.Errorf("spent = %d, want %d (the turn budget is denominated in ENCODED bytes, "+
			"because that is what the wire carries and the history replays)", spent, want)
	}
}

// TestAttachmentBlock_ImageDegradesToPathReference covers the two failures that
// must not lose the attachment: over the turn's cumulative budget, and over the
// per-file cap. Both fall back to a path the agent can read with its image tool.
func TestAttachmentBlock_ImageDegradesToPathReference(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "big.png")
	if err := os.WriteFile(abs, pngBytes(t, 64, 64), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("over_turn_budget", func(t *testing.T) {
		block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, Workspace{Dir: dir}, 10, true)
		if got := block[keyType]; got != marotte.ContentTypeText {
			t.Errorf("block type = %v, want text (a path reference)", got)
		}
		if spent != 0 {
			t.Errorf("spent = %d, want 0 for a block that inlined nothing", spent)
		}
	})

	t.Run("path_escapes_workspace", func(t *testing.T) {
		elsewhere := Workspace{Dir: t.TempDir()}
		block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, elsewhere, MaxInlineTurnEncodedBytes, true)
		if got := block[keyType]; got != marotte.ContentTypeText {
			t.Errorf("block type = %v, want text", got)
		}
		if spent != 0 {
			t.Errorf("spent = %d, want 0", spent)
		}
	})
}

// TestAttachmentBlock_TextFileTakesPathReference: a .txt takes the path-reference branch, which the
// large-paste spill (written as a .txt in uploads) depends on.
func TestAttachmentBlock_TextFileTakesPathReference(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "paste-2026-08-16T01-42-33.txt")
	if err := os.WriteFile(abs, []byte("a pasted log\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs, Name: filepath.Base(abs)},
		Workspace{Dir: dir}, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != marotte.ContentTypeText {
		t.Fatalf("block type = %v, want text (a path reference)", got)
	}
	wantText := "Attached file: " + abs
	if got := block["text"]; got != wantText {
		t.Errorf("text = %v, want %q", got, wantText)
	}
	if spent != 0 {
		t.Errorf("spent = %d, want 0: a path reference inlines nothing and so costs no budget", spent)
	}
	if _, isDoc := documentExts[".txt"]; isDoc {
		t.Error(".txt is in documentExts; a spilled paste would arrive as base64 instead of a readable file")
	}
	if unsupportedDocExts[".txt"] {
		t.Error(".txt is in unsupportedDocExts; it would carry a misleading binary-format note")
	}
}

// TestEncodedCapCannotBeSubsumedByTheFileCap asserts that pure arithmetic on the shipped constants; a file
// under MaxDocumentBytes can exceed MaxInlineEncodedBytes once base64'd.
func TestEncodedCapCannotBeSubsumedByTheFileCap(t *testing.T) {
	if got := base64.StdEncoding.EncodedLen(MaxDocumentBytes); got <= MaxInlineEncodedBytes {
		t.Errorf("a %d-byte file encodes to %d, which is inside the %d encoded cap: "+
			"the encoded gate is unreachable and the file cap subsumes it",
			MaxDocumentBytes, got, MaxInlineEncodedBytes)
	}
}

// TestReadForInline_ChargesTheBudgetInEncodedBytes pins the budget's UNIT with a tiny attachment,
// since the budget is threaded.
func TestReadForInline_ChargesTheBudgetInEncodedBytes(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "shot.png")
	pixels := pngBytes(t, 10, 10)
	if err := os.WriteFile(abs, pixels, 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Dir: dir}
	encoded := base64.StdEncoding.EncodedLen(len(pixels))

	t.Run("one encoded byte short is refused", func(t *testing.T) {
		block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, ws, encoded-1, true)
		if got := block[keyType]; got != "text" {
			t.Errorf("block type = %v, want a text path reference; the budget is in "+
				"encoded bytes and %d of them do not fit in %d", got, encoded, encoded-1)
		}
		if spent != 0 {
			t.Errorf("spent = %d on a refused attachment, want 0", spent)
		}
	})

	t.Run("exactly the encoded length fits", func(t *testing.T) {
		// Exactly-at-cap must be accepted: the classic off-by-one mutant.
		block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, ws, encoded, true)
		if got := block[keyType]; got != "image" {
			t.Errorf("block type = %v, want the image inlined at exactly its encoded cost", got)
		}
		if spent != encoded {
			t.Errorf("spent = %d, want %d", spent, encoded)
		}
	})
}

// TestReadForInline_AnImageOverTheEncodedCapIsFittedNotRefused is the image half
// of the encoded cap: a picture whose raw bytes would encode past
// MaxInlineEncodedBytes is re-encoded smaller and inlined, where the document
// half of the same gate still refuses (TestReadForInline_EncodedCapAppliesToDocumentsToo).
func TestReadForInline_AnImageOverTheEncodedCapIsFittedNotRefused(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "photo.png")
	raw := noisePNG(t, 1024, 1024)
	if base64.StdEncoding.EncodedLen(len(raw)) <= MaxInlineEncodedBytes || len(raw) > MaxDocumentBytes {
		t.Fatalf("Setup: fixture of %d bytes must encode past %d and stay under the %d file cap",
			len(raw), MaxInlineEncodedBytes, MaxDocumentBytes)
	}
	if err := os.WriteFile(abs, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs},
		Workspace{Dir: dir}, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != "image" {
		t.Fatalf("attachmentBlock(%d-byte png) type = %v, want an image block fitted under the cap; text = %q",
			len(raw), got, block["text"])
	}
	data := decodeBlockData(t, block)
	if got := base64.StdEncoding.EncodedLen(len(data)); got > MaxInlineEncodedBytes || spent != got {
		t.Errorf("fitted payload encodes to %d with spent = %d, want both equal and <= %d", got, spent, MaxInlineEncodedBytes)
	}
	if got := block["mimeType"]; got != "image/png" {
		t.Errorf("mimeType = %v, want image/png", got)
	}
}

// TestReadForInline_OversizedFileNamesThePath is the message-quality half, and it
// is a real defect rather than polish: the per-FILE cap's fallback handed the agent
// `filepath.Base(path)`, so the one branch that most needs to be actionable — the
// contents can only be reached by opening the file — was the one that did not say
// where the file is.
func TestReadForInline_OversizedFileNamesThePath(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "huge.pdf")
	if err := os.WriteFile(abs, make([]byte, MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Dir: dir}

	block, _ := attachmentBlock(t.Context(), marotte.Attachment{Path: abs},
		ws, MaxInlineTurnEncodedBytes, true)

	text, _ := block["text"].(string)
	if !strings.Contains(text, abs) {
		t.Errorf("fallback text %q carries only a basename; a file tool cannot open that", text)
	}
}

// TestReadForInline_EncodedCapAppliesToDocumentsToo: the gate lives in the shared gauntlet, not the
// image branch.
func TestReadForInline_EncodedCapAppliesToDocumentsToo(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "book.pdf")
	size := MaxInlineEncodedBytes/4*3 + 1
	if err := os.WriteFile(abs, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Dir: dir}

	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs},
		ws, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != "text" {
		t.Errorf("block type = %v, want a text path reference; the encoded cap is not "+
			"image-only", got)
	}
	if spent != 0 {
		t.Errorf("spent = %d on a refused attachment, want 0", spent)
	}
	text, _ := block["text"].(string)
	if !strings.Contains(text, abs) || !strings.Contains(text, "file tools") {
		t.Errorf("fallback text %q must carry the full path and name the file tools: "+
			"they are the agent's only route to the contents now", text)
	}
	if !strings.Contains(text, "may not be readable as text") {
		t.Errorf("fallback note %q tells the agent to read a PDF with no hint the bytes are "+
			"binary, while the unsupportedDocExts branch carries that caveat for the same class", text)
	}
}

// The companion of TestReadForInline_EncodedCapAppliesToDocumentsToo: that one
// uses the smallest file whose encoding does NOT fit, this one the largest that
// does. Exactly at the cap must be inlined, and charged at exactly the cap.
func TestReadForInline_AcceptsAnEncodedPayloadExactlyAtTheCap(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "book.pdf")
	size := MaxInlineEncodedBytes / 4 * 3
	if err := os.WriteFile(abs, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if encoded := base64.StdEncoding.EncodedLen(size); encoded != MaxInlineEncodedBytes {
		t.Fatalf("fixture of %d bytes encodes to %d, want exactly %d; the test is not "+
			"at the boundary it claims", size, encoded, MaxInlineEncodedBytes)
	}
	ws := Workspace{Dir: dir}

	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs},
		ws, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != "resource" {
		t.Errorf("block type = %v, want the document inlined: %d bytes encode to exactly "+
			"the %d cap", got, size, MaxInlineEncodedBytes)
	}
	if spent != MaxInlineEncodedBytes {
		t.Errorf("spent = %d, want %d", spent, MaxInlineEncodedBytes)
	}
}

// TestReadForInline_OversizedImageDoesNotSendTheAgentToItsFileTools asserts that MaxDocumentBytes is KAS's
// MAX_IMAGE_SIZE, so the image tool would refuse the file too.
func TestReadForInline_OversizedImageDoesNotSendTheAgentToItsFileTools(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(abs, make([]byte, MaxDocumentBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Dir: dir}

	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, ws, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != marotte.ContentTypeText {
		t.Fatalf("block type = %v, want text (a path reference)", got)
	}
	text, _ := block["text"].(string)
	if strings.Contains(text, "file tools") {
		t.Errorf("fallback text %q sends the agent to its file tools, but MaxDocumentBytes (%d) "+
			"is KAS's MAX_IMAGE_SIZE, so the image tool refuses this file too", text, MaxDocumentBytes)
	}
	if !strings.Contains(text, "resized") {
		t.Errorf("fallback text %q does not name the only remedy left, a smaller image", text)
	}
	if !strings.Contains(text, abs) {
		t.Errorf("fallback text %q carries only a basename; the user cannot tell which file to resize", text)
	}
	if spent != 0 {
		t.Errorf("spent = %d on a refused attachment, want 0", spent)
	}
}

// TestReadForInline_BudgetNoteFlagsBinaryBytesOnly asserts that the budget refusal's read-the-file advice is
// flagged only for binary formats.
func TestReadForInline_BudgetNoteFlagsBinaryBytesOnly(t *testing.T) {
	const caveat = "may not be readable as text"
	for _, tc := range []struct {
		name       string
		file       string
		wantCaveat bool
	}{
		{name: "binary_document", file: "report.pdf", wantCaveat: true},
		{name: "binary_spreadsheet", file: "book.xlsx", wantCaveat: true},
		{name: "text_document", file: "rows.csv", wantCaveat: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			abs := filepath.Join(dir, tc.file)
			if err := os.WriteFile(abs, make([]byte, 4096), 0o600); err != nil {
				t.Fatal(err)
			}
			ws := Workspace{Dir: dir}

			block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, ws, 10, true)

			if got := block[keyType]; got != marotte.ContentTypeText {
				t.Fatalf("attachmentBlock(%q) type = %v, want text (a path reference)", tc.file, got)
			}
			text, _ := block["text"].(string)
			if got := strings.Contains(text, caveat); got != tc.wantCaveat {
				t.Errorf("attachmentBlock(%q) note = %q, carries the binary caveat = %v, want %v",
					tc.file, text, got, tc.wantCaveat)
			}
			if !strings.Contains(text, "budget is spent") {
				t.Errorf("attachmentBlock(%q) note = %q, want it to name the turn budget as the cause",
					tc.file, text)
			}
			if !strings.Contains(text, "file tools") {
				t.Errorf("attachmentBlock(%q) note = %q, want it to still name the file tools: "+
					"a document under the size caps is readable, unlike an oversize image", tc.file, text)
			}
			if spent != 0 {
				t.Errorf("attachmentBlock(%q) spent = %d on a refused attachment, want 0", tc.file, spent)
			}
		})
	}
}

func TestBuildPromptBlocks_HistoryImageBudgetDegradesToPathReference(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "next.png")
	if err := os.WriteFile(abs, pngBytes(t, 4, 4), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Dir: dir}

	blocks := BuildPromptBlocks(t.Context(), "see this", []marotte.Attachment{{Path: abs}}, MaxHistoryInlineImages, ws, nil)
	if got := blocks[1][keyType]; got != marotte.ContentTypeText {
		t.Errorf("BuildPromptBlocks history-budget block type = %v, want text", got)
	}
	text, _ := blocks[1]["text"].(string)
	if !strings.Contains(text, "replayed image history budget") {
		t.Errorf("BuildPromptBlocks history-budget note = %q, want the history reason", text)
	}
}

func TestBuildPromptBlocks_AnAttachmentOnlyPromptSendsNoEmptyTextPart(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(abs, pngBytes(t, 4, 4), 0o600); err != nil {
		t.Fatal(err)
	}
	blocks := BuildPromptBlocks(t.Context(), "", []marotte.Attachment{{Path: abs}}, 0, Workspace{Dir: dir}, nil)
	if len(blocks) != 1 {
		t.Fatalf("BuildPromptBlocks(\"\", 1 image) = %d blocks, want 1", len(blocks))
	}
	if got := blocks[0][keyType]; got != "image" {
		t.Errorf("BuildPromptBlocks(\"\", 1 image) first block type = %v, want image", got)
	}
	if got := BuildPromptBlocks(t.Context(), "", nil, 0, Workspace{Dir: dir}, nil); len(got) != 1 || got[0][keyType] != marotte.ContentTypeText {
		t.Errorf("BuildPromptBlocks(\"\", none) = %v, want the one text block", got)
	}
}

// attachmentPathsStore answers PromptAttachmentPaths from a fixed list, or with
// an error: the store owns the watermark cut, this counts what it answers.
type attachmentPathsStore struct {
	*testsupport.InMemoryChatStore
	paths []string
	err   error
}

func (s *attachmentPathsStore) PromptAttachmentPaths(context.Context, marotte.ChatID, string) ([]string, error) {
	return s.paths, s.err
}

func TestHistoryInlineImages_CountsTheImagesTheStoreAnswers(t *testing.T) {
	store := &attachmentPathsStore{
		InMemoryChatStore: testsupport.NewInMemoryChatStore(),
		paths:             []string{"old.png", "notes.pdf", "shot.webp", "diagram.svg"},
	}
	seedEmptyChat(t, store, "c1")

	if got := historyInlineImages(t.Context(), store, "c1"); got != 2 {
		t.Errorf("historyInlineImages = %d, want 2: two of the four prompt attachments are images", got)
	}
}

// A history that cannot be read answers the cap, so a doubt costs a path
// reference rather than a refused prompt; so does a chat with no record.
func TestHistoryInlineImages_ADoubtAnswersTheCap(t *testing.T) {
	failing := &attachmentPathsStore{InMemoryChatStore: testsupport.NewInMemoryChatStore(), err: errors.New("log unreadable")}
	seedEmptyChat(t, failing, "c1")
	if got := historyInlineImages(t.Context(), failing, "c1"); got != MaxHistoryInlineImages {
		t.Errorf("historyInlineImages on an unreadable log = %d, want the cap %d", got, MaxHistoryInlineImages)
	}
	if got := historyInlineImages(t.Context(), testsupport.NewInMemoryChatStore(), "absent"); got != MaxHistoryInlineImages {
		t.Errorf("historyInlineImages on a missing chat = %d, want the cap %d", got, MaxHistoryInlineImages)
	}
}

func TestBuildPromptBlocks_HistoryImageBudgetDoesNotAffectDocuments(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(abs, []byte("%PDF-1.7"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := Workspace{Dir: dir}

	blocks := BuildPromptBlocks(t.Context(), "read this", []marotte.Attachment{{Path: abs}}, MaxHistoryInlineImages, ws, nil)
	if got := blocks[1][keyType]; got != "resource" {
		t.Errorf("BuildPromptBlocks document type at image budget = %v, want resource", got)
	}
}

// An image the fit refuses keeps its own reason at a spent history budget: the
// history gate runs last, so it never hides why the image itself was refused.
func TestBuildPromptBlocks_AFitRefusalIsNotHiddenByTheHistoryGate(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(abs, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}

	blocks := BuildPromptBlocks(t.Context(), "see this", []marotte.Attachment{{Path: abs}}, MaxHistoryInlineImages, Workspace{Dir: dir}, nil)
	text, _ := blocks[1]["text"].(string)
	if !strings.Contains(text, "could not be decoded") {
		t.Errorf("BuildPromptBlocks fit-refusal note = %q, want the image's own reason", text)
	}
	if strings.Contains(text, "history budget") {
		t.Errorf("BuildPromptBlocks fit-refusal note = %q, history gate hid the per-attachment reason", text)
	}
}

// An image whose RAW size would not fit the turn budget is still inlined when its
// fitted payload does: the raw size says nothing about what is sent.
func TestBuildPromptBlocks_ImageBudgetIsChargedOnTheFittedPayload(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "wide.png")
	raw := uncompressedPNG(t, 2500, 900)
	if err := os.WriteFile(abs, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	budget := 1 << 20
	if base64.StdEncoding.EncodedLen(len(raw)) <= budget {
		t.Fatalf("Setup: raw fixture encodes to %d, want it over the %d budget", base64.StdEncoding.EncodedLen(len(raw)), budget)
	}

	block, spent := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, Workspace{Dir: dir}, budget, true)

	if got := block[keyType]; got != "image" {
		t.Fatalf("attachmentBlock(raw %d bytes, budget %d) type = %v, text = %q; want the fitted image inlined",
			len(raw), budget, got, block["text"])
	}
	if spent > budget || spent == 0 {
		t.Errorf("spent = %d, want the fitted payload's encoded size, within %d", spent, budget)
	}
}

func TestAttachmentBlock_OverEdgeImageIsDownscaledToTheCap(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "screenshot.png")
	if err := os.WriteFile(abs, pngBytes(t, 3000, 1000), 0o600); err != nil {
		t.Fatal(err)
	}

	block, _ := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, Workspace{Dir: dir}, MaxInlineTurnEncodedBytes, true)

	if got := block[keyType]; got != "image" {
		t.Fatalf("attachmentBlock(3000x1000 png) type = %v, text = %q; want an image block", got, block["text"])
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(decodeBlockData(t, block)))
	if err != nil {
		t.Fatalf("decoding the block's image: %v", err)
	}
	if cfg.Width != 2000 || cfg.Height != 667 {
		t.Errorf("attachmentBlock(3000x1000 png) sent %dx%d, want 2000x667", cfg.Width, cfg.Height)
	}
}

// A FIFO at an attachment path must not block the turn in open(2): the read runs
// with the turn slot and the bridge's prompt lock held, and the open ignores the
// context.
func TestReadForInline_RefusesAFifoInsteadOfBlocking(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "x.pdf")
	if err := syscall.Mkfifo(abs, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}

	done := make(chan map[string]any, 1)
	go func() {
		block, _ := attachmentBlock(t.Context(), marotte.Attachment{Path: abs}, Workspace{Dir: dir}, MaxInlineTurnEncodedBytes, true)
		done <- block
	}()
	select {
	case block := <-done:
		if text, _ := block["text"].(string); text != "Attached file (unreadable): x.pdf" {
			t.Errorf("attachmentBlock(fifo) text = %q, want the unreadable note", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attachmentBlock(fifo) still blocked after 5s")
	}
}

// The confinement verdict and the open must name the same object. A component
// swapped for a symlink between the two must not be followed out of the root.
func TestReadForInline_DoesNotFollowAnAncestorSwappedOutsideTheRoot(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(sub, "a.pdf")
	if err := os.WriteFile(inside, []byte("%PDF-inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := []byte("%PDF-outside-secret")
	if err := os.WriteFile(filepath.Join(outside, "a.pdf"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	w := Workspace{Dir: ws}
	root, rel, err := workspace.ConfineAnyAbs(w.attachmentRoots(), inside)
	if err != nil {
		t.Fatalf("Setup: ConfineAnyAbs: %v", err)
	}

	if err := os.Rename(sub, sub+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sub); err != nil {
		t.Fatal(err)
	}

	if f, _, err := openConfined(root, rel); err == nil {
		got, _ := io.ReadAll(f)
		f.Close()
		t.Errorf("openConfined(%q, %q) after the swap = %q, want an error rather than the outside file", root, rel, got)
	}
	block, _ := attachmentBlock(t.Context(), marotte.Attachment{Path: inside}, w, MaxInlineTurnEncodedBytes, true)
	if res, ok := block["resource"].(map[string]any); ok && res["blob"] == base64.StdEncoding.EncodeToString(secret) {
		t.Error("attachmentBlock read the file the swapped symlink points at, outside the workspace")
	}
}

func TestValidationGuidance_AToolReturnedImageIsRewoundNotReopened(t *testing.T) {
	re := &marotte.RPCError{Code: -32603, Message: "ImageDimensionExceeded"}
	for _, inlined := range []bool{true, false} {
		text := validationGuidance(re, inlined)
		if !strings.Contains(text, "Rewind") {
			t.Errorf("validationGuidance(inlinedImage=%v) = %q, want it to name Rewind", inlined, text)
		}
		if strings.Contains(text, "Reopen the chat if") || strings.Contains(text, "reopen the chat to clear") {
			t.Errorf("validationGuidance(inlinedImage=%v) = %q advises reopening; a tool-returned image survives a reload", inlined, text)
		}
	}
}

// pngBytes encodes a w x h opaque image as PNG.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 40, 90, 160, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("Setup: png.Encode: %v", err)
	}
	return buf.Bytes()
}

// noisePNG encodes w x h random pixels, which PNG cannot compress, so the file
// size tracks the pixel count.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(rnd.Uint32())
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("Setup: png.Encode: %v", err)
	}
	return buf.Bytes()
}

// uncompressedPNG encodes a w x h solid image with compression off, so a small
// fitted re-encode is far smaller than the file.
func uncompressedPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&buf, img); err != nil {
		t.Fatalf("Setup: png.Encode: %v", err)
	}
	return buf.Bytes()
}

func decodeBlockData(t *testing.T, block map[string]any) []byte {
	t.Helper()
	s, _ := block["data"].(string)
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("block data is not base64: %v", err)
	}
	return data
}
