package chat

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/pathinside/v2"
)

// openChatFile opens path for reading with the FileInfo the open produced. OpenRegular, not os.Open: open(2) on a FIFO
// blocks with no deadline (go1.27.0), and the agent's shell can write this directory, so one mkfifo wedges every reader.
func openChatFile(path, label string) (*os.File, os.FileInfo, error) {
	// For CodeQL's go/path-injection analyzer, which does not follow ValidChatID across packages. It runs on the cleaned
	// value, so the traversal test cannot fire.
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return nil, nil, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	return atomicfile.OpenRegular(clean)
}

// writeHeadroomFraction is how close to the cap a successful append may land before it is reported: the last tenth of
// a chat's budget, reported on the write itself.
const writeHeadroomFraction = 10
