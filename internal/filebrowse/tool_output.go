package filebrowse

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// toolOutputName is KAS's writeToolOutput file name: the tool id with every byte
// outside [A-Za-z0-9_-] replaced, a dash, and the first 8 hex digits of a UUID.
var toolOutputName = regexp.MustCompile(`^[A-Za-z0-9_-]+-[0-9a-f]{8}\.txt$`)

// AllowToolOutputs grants read-only access to the large tool outputs KAS offloads
// under sessionsDir (`<KiroHome>/sessions`), which the /config/home deny-list
// otherwise blocks. Only `<bucket>/sess_*/tool-outputs/<tool>-<8hex>.txt` is
// readable; nothing there is writable, deletable or listable.
func (h *Handler) AllowToolOutputs(sessionsDir string) {
	h.toolOutputs = filepath.Clean(sessionsDir)
}

// toolOutput is an opened KAS tool-output file. The caller closes f.
type toolOutput struct {
	f    *os.File
	info fs.FileInfo
	abs  string
}

// openToolOutput opens the KAS tool-output file reqPath names; granted is false when reqPath is not
// one, including a symlink or non-regular file at any component. Opened once, component by
// component, so the bytes served are the file the shape was checked against.
func (h *Handler) openToolOutput(reqPath string) (out toolOutput, granted bool, err error) {
	if h.toolOutputs == "" {
		return toolOutput{}, false, nil
	}
	clean := filepath.Clean("/" + reqPath)
	rel, ok := strings.CutPrefix(clean, h.toolOutputs+"/")
	if !ok || !toolOutputRel(rel) {
		return toolOutput{}, false, nil
	}
	// Opened per request because KAS creates the sessions directory after boot.
	root, err := os.OpenRoot(h.toolOutputs)
	if err != nil {
		return toolOutput{abs: clean}, true, err
	}
	defer root.Close()
	f, err := openPinnedRoot(loc{m: &mount{root: root, dir: h.toolOutputs}, abs: clean})
	if err != nil {
		if isSwapRefusal(err) {
			return toolOutput{}, false, nil
		}
		return toolOutput{abs: clean}, true, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return toolOutput{abs: clean}, true, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return toolOutput{}, false, nil
	}
	return toolOutput{f: f, info: info, abs: clean}, true, nil
}

func toolOutputRel(rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) == 4 &&
		parts[0] != "" && !strings.HasPrefix(parts[0], ".") &&
		strings.HasPrefix(parts[1], "sess_") && fs.ValidPath(parts[1]) &&
		parts[2] == "tool-outputs" &&
		toolOutputName.MatchString(parts[3])
}
