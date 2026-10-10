package filebrowse

// FileStat is GET /api/file/stat's reply: the facts the viewer picks a view from.
type FileStat struct {
	Path string `json:"path"`
	// FileID is the identity of a file at or below WholeFileMax, absent for a larger one.
	FileID   string `json:"file_id,omitempty"`
	Modified string `json:"modified"`
	Size     int64  `json:"size"`
	// Large is the server's verdict that the file is over WholeFileMax.
	Large  bool `json:"large"`
	Binary bool `json:"binary"`
	// UTF8 is false for a large file, which is not read.
	UTF8     bool `json:"utf8"`
	ReadOnly bool `json:"read_only"`
}

// FileRead is GET /api/file's reply: the whole file, at or below WholeFileMax. Content is
// the bytes as a string; when UTF8 is false JSON has replaced the invalid sequences, so
// the client must not offer to save it.
type FileRead struct {
	Path     string `json:"path"`
	FileID   string `json:"file_id"`
	Modified string `json:"modified"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
	UTF8     bool   `json:"utf8"`
	ReadOnly bool   `json:"read_only"`
}

// FileWriteResult is PUT /api/file's success reply; FileID is the identity of the bytes
// this request wrote.
type FileWriteResult struct {
	FileID string `json:"file_id"`
	Size   int64  `json:"size"`
	OK     bool   `json:"ok"`
}

// RefusalCode is the machine-readable reason on a FileRefusal.
type RefusalCode string

const (
	// RefusalChanged is a file that moved under the read, or whose identity differs from
	// the one the request carried.
	RefusalChanged RefusalCode = "changed"
	// RefusalTooLarge is a file or a body over WholeFileMax.
	RefusalTooLarge RefusalCode = "too_large"
	// RefusalBinary is a file with a NUL in its first 8 KiB.
	RefusalBinary RefusalCode = "binary"
	// RefusalInvalidFileID is a malformed file_id parameter or body field.
	RefusalInvalidFileID RefusalCode = "invalid_file_id"
)

// ContentKind classifies the bytes on disk behind a refused stale save.
type ContentKind string

const (
	// ContentText is valid UTF-8 with no NUL; the refusal carries the content.
	ContentText ContentKind = "text"
	// ContentBinary has a NUL in its first 8 KiB; no content is sent.
	ContentBinary ContentKind = "binary"
	// ContentNotUTF8 is invalid UTF-8, which JSON would rewrite; no content is sent.
	ContentNotUTF8 ContentKind = "not_utf8"
	// ContentTooLarge is a file now over WholeFileMax; no content is sent.
	ContentTooLarge ContentKind = "too_large"
)

// FileRefusal is the error body of the viewer routes that a client branches on.
type FileRefusal struct {
	// Content is the disk text behind a refused save, present only for ContentText.
	Content     *string     `json:"content,omitempty"`
	Size        *int64      `json:"size,omitempty"`
	Error       string      `json:"error"`
	Code        RefusalCode `json:"code"`
	FileID      string      `json:"file_id,omitempty"`
	ContentKind ContentKind `json:"content_kind,omitempty"`
}
