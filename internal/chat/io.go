package chat

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/atomicfile/v4"
	"github.com/cplieger/pathinside/v2"
)

// openChatFile opens path for reading, with the FileInfo the open produced. OpenRegular and NOT
// os.Open: os.Open on a FIFO blocks in open(2) with no deadline able to rescue it (go1.27.0),
// and this directory is writable by the agent's own shell, so one mkfifo wedges every reader.
func openChatFile(path, label string) (*os.File, os.FileInfo, error) {
	// For CodeQL's go/path-injection analyzer, which does not follow ValidChatID across packages.
	// KNOWN VACUITY: it runs on the CLEANED value, so the traversal test cannot fire.
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || pathinside.HasDotDot(clean) {
		return nil, nil, fmt.Errorf("%s: rejected unsafe path %q", label, path)
	}
	return atomicfile.OpenRegular(clean)
}

// writeHeadroomFraction is how close to the cap a SUCCESSFUL append may land before
// it is reported. A tenth gives an operator the last 10% of a chat's budget to act
// in, and the alarm rides the write it describes rather than a poll nothing
// schedules.
const writeHeadroomFraction = 10
