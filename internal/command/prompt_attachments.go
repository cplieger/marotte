package command

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/marotte/internal/marotte"
)

// documentExts maps an extension to the MIME type of the ACP embedded `resource` block it inlines
// as. It must stay a subset of KAS's SUPPORTED_DOCUMENT_MIME_TYPES (TestDocumentExtsSubsetOfKAS):
// extractContentFromPrompt silently drops any other MIME after it was read and sent.
// html/plain/markdown are omitted so the agent reads them with its file tools.
var documentExts = map[string]string{
	".pdf":  "application/pdf",
	".csv":  "text/csv",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// unsupportedDocExts are binary document formats KAS's inline-document
// allowlist does not accept, so they route through the path-reference
// branch instead of being silently dropped.
var unsupportedDocExts = map[string]bool{
	".pptx": true,
	".ppt":  true,
	".rtf":  true,
	".odt":  true,
	".ods":  true,
	".odp":  true,
}

// imageExts maps an image extension to the MIME type of the ACP `image` block it inlines as,
// mirroring KAS's IMAGE_EXTENSIONS (KAS pushes image blocks unconditionally). Not svg or avif: KAS
// excludes both. Not unified with utils-url.ts's INLINE_IMAGE_EXT, which answers what a browser
// renders inline.
var imageExts = map[string]string{
	".png":  mimePNG,
	".jpg":  mimeJPEG,
	".jpeg": mimeJPEG,
	".gif":  mimeGIF,
	".webp": mimeWebP,
}

// MaxDocumentBytes caps the size of one document or image attachment
// (10 MiB), also KAS's own MAX_IMAGE_SIZE. Kept in file bytes: the number a
// user can predict from a file listing, and the cheap pre-read gate.
const MaxDocumentBytes = 10 * 1024 * 1024

// MaxInlineEncodedBytes caps the base64 payload of one inlined attachment; MaxDocumentBytes cannot,
// since base64 inflates by 4/3. Inferred from KiroCrew's reading of the backend's refusal text:
// erring low only ships a smaller image.
const MaxInlineEncodedBytes = 5 * 1024 * 1024

// MaxInlineTurnEncodedBytes caps the total encoded bytes one prompt may
// inline. Counted in encoded bytes since that is what the request carries.
const MaxInlineTurnEncodedBytes = 15 * 1024 * 1024

// MaxHistoryInlineImages caps user-prompt images still present after the last
// compaction. The value is inferred from the backend's approximately 20-image
// rejection point. Paths count even when an earlier size gate refused them, and
// a session change can leave stale paths; both errors fail closed.
const MaxHistoryInlineImages = 16

// BuildPromptBlocks constructs the ACP prompt content array: a leading text block (omitted for an
// attachment-only prompt, since KAS refuses an empty text part), then one block per attachment: a
// supported document as an embedded `resource`, an image as an `image` fitted by fitImage, anything
// else a path reference. mcp is the prompt's bridge, so an MCP reference reads that chat's pool;
// nil leaves them unresolved.
func BuildPromptBlocks(ctx context.Context, text string, attachments []marotte.Attachment, historyImages int, ws Workspace, mcp bridgeCaller) []map[string]any {
	blocks := make([]map[string]any, 0, 1+len(attachments))
	if strings.TrimSpace(text) != "" || len(attachments) == 0 {
		blocks = append(blocks, marotte.TextBlock(text))
	}
	blocks = append(blocks, mentionBlocks(ctx, text, ws, mcp)...)
	budget := MaxInlineTurnEncodedBytes
	imageAllowance := max(MaxHistoryInlineImages-historyImages, 0)
	for _, att := range attachments {
		if ctx.Err() != nil {
			return blocks
		}
		block, spent := attachmentBlock(ctx, att, ws, budget, imageAllowance > 0)
		budget -= spent
		blocks = append(blocks, block)
		imageAllowance -= inlineImageBlockCount([]map[string]any{block})
	}
	return blocks
}

func isImagePath(path string) bool {
	_, ok := imageExts[strings.ToLower(filepath.Ext(path))]
	return ok
}

func inlineImageBlockCount(blocks []map[string]any) int {
	count := 0
	for _, block := range blocks {
		if block[keyType] == "image" {
			count++
		}
	}
	return count
}

// attachmentBlock builds the ACP content block for one attachment and returns the encoded bytes it
// consumed from the turn's inline budget (zero for a path reference).
func attachmentBlock(ctx context.Context, att marotte.Attachment, ws Workspace, budget int, allowImage bool) (block map[string]any, spentBytes int) {
	displayName := filepath.Base(att.Path)
	ext := strings.ToLower(filepath.Ext(att.Path))

	if mime, isDoc := documentExts[ext]; isDoc {
		return inlineResourceBlock(ctx, att, displayName, mime, ws, budget)
	}
	if mime, isImg := imageExts[ext]; isImg {
		return inlineImageBlock(ctx, att, displayName, mime, ws, budget, allowImage)
	}

	// Path-reference branch: validate containment only. marotte does not read
	// this file; the agent does, through its own policy.
	if _, err := ws.ResolveInside(att.Path); err != nil {
		slog.Warn("attachment: path escapes workspace",
			"path", displayName, keyError, err)
		return marotte.TextBlock("Attached file (invalid path): " + displayName), 0
	}
	if unsupportedDocExts[ext] {
		return marotte.TextBlock("Attached file: " + att.Path +
			" (binary document — read it with your file tools; this format may not be readable as text)"), 0
	}
	return marotte.TextBlock("Attached file: " + att.Path), 0
}

// inlinePayload is what an attachment that passed every gate sends: the bytes,
// their MIME type, and the absolute path they were read from.
type inlinePayload struct {
	abs  string
	mime string
	data []byte
}

// inlineResourceBlock reads a document attachment and returns an ACP embedded `resource` block (the
// blob variant; v3 has no `document` type), or a descriptive text block on failure.
func inlineResourceBlock(ctx context.Context, att marotte.Attachment, displayName, mime string, ws Workspace, budget int) (block map[string]any, spentBytes int) {
	in, fallback := readForInline(ctx, att, displayName, mime, ws, budget, true)
	if fallback != nil {
		return fallback, 0
	}
	return map[string]any{
		keyType: keyResource,
		keyResource: map[string]any{
			keyURI:      "file://" + in.abs,
			keyMimeType: in.mime,
			"blob":      base64.StdEncoding.EncodeToString(in.data),
		},
	}, base64.StdEncoding.EncodedLen(len(in.data))
}

// inlineImageBlock reads an image attachment and returns an ACP `image` block with the fitted bytes
// and MIME type; failure degrades to a path reference. No `uri`: KAS's toDataUrl returns a present
// uri instead of building the data URL.
func inlineImageBlock(ctx context.Context, att marotte.Attachment, displayName, mime string, ws Workspace, budget int, allowImage bool) (block map[string]any, spentBytes int) {
	in, fallback := readForInline(ctx, att, displayName, mime, ws, budget, allowImage)
	if fallback != nil {
		return fallback, 0
	}
	return map[string]any{
		keyType:    "image",
		"data":     base64.StdEncoding.EncodeToString(in.data),
		"mimeType": in.mime,
	}, base64.StdEncoding.EncodedLen(len(in.data))
}

// readForInline runs the gauntlet every inlined attachment passes (one confined open, the per-file
// cap, the read, the per-kind gates) and returns the payload or the text block to send instead.
// Sizes come from the opened descriptor, so the file judged is the file read and a FIFO or device
// is refused.
func readForInline(
	ctx context.Context,
	att marotte.Attachment,
	displayName, mime string,
	ws Workspace,
	budget int,
	allowImage bool,
) (payload inlinePayload, fallback map[string]any) {
	isImage := strings.HasPrefix(mime, "image/")
	// text/csv is the one documentExts member whose bytes a file tool
	// reads as text, so the caveat is keyed on MIME.
	isBinaryDoc := !isImage && !strings.HasPrefix(mime, "text/")

	f, info, abs, err := ws.OpenAttachment(att.Path)
	if err != nil {
		if errors.Is(err, errAttachmentOutsideRoots) {
			slog.Warn("attachment: path escapes workspace",
				"path", displayName, keyError, err)
			return inlinePayload{}, marotte.TextBlock("Attached file (invalid path): " + displayName)
		}
		slog.Warn("attachment: open failed", "path", displayName, keyError, err)
		return inlinePayload{}, marotte.TextBlock("Attached file (unreadable): " + displayName)
	}
	defer f.Close()
	if info.Size() > MaxDocumentBytes {
		slog.Warn("attachment: too large",
			"path", displayName, "size", info.Size(), "cap", MaxDocumentBytes)
		return inlinePayload{}, tooLargeFileBlock(att.Path, isImage)
	}
	// Documents are estimated before the read, in the budget's encoded unit; an image is charged on
	// its fitted payload.
	if !isImage && base64.StdEncoding.EncodedLen(int(info.Size())) > budget {
		slog.Warn("attachment: turn inline budget exhausted, sending a path reference",
			"path", displayName, "size", info.Size(), "remaining_encoded", budget)
		return inlinePayload{}, budgetSpentBlock(att.Path, isBinaryDoc)
	}
	data, err := atomicfile.ReadBoundedFile(ctx, f, MaxDocumentBytes)
	if errors.Is(err, atomicfile.ErrFileTooLarge) {
		slog.Warn("attachment: grew past the cap during the read",
			"path", displayName, "cap", MaxDocumentBytes)
		return inlinePayload{}, tooLargeFileBlock(att.Path, isImage)
	}
	if err != nil {
		slog.Warn("attachment: read failed",
			"path", displayName, keyError, err)
		return inlinePayload{}, marotte.TextBlock("Attached file (unreadable): " + displayName)
	}
	if isImage {
		return fitForInline(att, displayName, abs, data, budget, allowImage)
	}
	// After the read, on len(data): no pre-read check sees base64's 4/3 inflation. EncodedLen
	// avoids allocating the encoding.
	if encoded := base64.StdEncoding.EncodedLen(len(data)); encoded > MaxInlineEncodedBytes {
		slog.Warn("attachment: encoded payload over cap, sending a path reference",
			"path", displayName, "size", len(data), "encoded", encoded, "cap", MaxInlineEncodedBytes)
		if isBinaryDoc {
			return inlinePayload{}, marotte.TextBlock("Attached file: " + att.Path +
				" (too large to inline — read it with your file tools; this format may not be readable as text)")
		}
		return inlinePayload{}, marotte.TextBlock("Attached file: " + att.Path +
			" (too large to inline — read it with your file tools)")
	}
	return inlinePayload{abs: abs, mime: mime, data: data}, nil
}

// fitForInline is the image half of readForInline: fit the bytes, then charge
// the turn budget and the replayed-history allowance on what will be sent.
func fitForInline(att marotte.Attachment, displayName, abs string, data []byte, budget int, allowImage bool) (payload inlinePayload, fallback map[string]any) {
	out, outMime, reason := fitImage(data, MaxImageEdgePx, MaxInlineEncodedBytes)
	if reason != "" {
		slog.Warn("attachment: image could not be fitted, sending a path reference",
			"path", displayName, "size", len(data), "reason", reason)
		return inlinePayload{}, marotte.TextBlock("Attached file: " + att.Path +
			" (not inlined: " + reason + "; read it with your file tools)")
	}
	if encoded := base64.StdEncoding.EncodedLen(len(out)); encoded > budget {
		slog.Warn("attachment: turn inline budget exhausted, sending a path reference",
			"path", displayName, "encoded", encoded, "remaining_encoded", budget)
		return inlinePayload{}, budgetSpentBlock(att.Path, false)
	}
	if !allowImage {
		slog.Warn("attachment: replayed image history budget exhausted, sending a path reference",
			"path", displayName, "cap", MaxHistoryInlineImages)
		return inlinePayload{}, marotte.TextBlock("Attached file: " + att.Path +
			" (not inlined: this chat's replayed image history budget is spent — read it with your file tools)")
	}
	return inlinePayload{abs: abs, mime: outMime, data: out}, nil
}

// tooLargeFileBlock is the path reference for a file over MaxDocumentBytes. It
// names att.Path rather than the basename, because this branch needs a path a
// file tool can open.
func tooLargeFileBlock(path string, isImage bool) map[string]any {
	if isImage {
		// MaxDocumentBytes is KAS's own MAX_IMAGE_SIZE, so the image tool
		// refuses at the same threshold; the only remedy is a smaller file.
		return marotte.TextBlock("Attached file: " + path +
			" (too large to inline, and the image tool refuses it at this size too — attach a smaller or resized image)")
	}
	return marotte.TextBlock("Attached file: " + path +
		" (too large to inline — read it with your file tools)")
}

func budgetSpentBlock(path string, isBinaryDoc bool) map[string]any {
	if isBinaryDoc {
		return marotte.TextBlock("Attached file: " + path +
			" (not inlined: this turn's attachment budget is spent — read it with your file tools; this format may not be readable as text)")
	}
	return marotte.TextBlock("Attached file: " + path +
		" (not inlined: this turn's attachment budget is spent — read it with your file tools)")
}
