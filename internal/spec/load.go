package spec

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/marotte/internal/marotte"
)

// ErrNoDocs reports a spec directory holding no markdown document.
var ErrNoDocs = errors.New("spec: directory holds no markdown document")

// Rank orders a spec directory's documents for display: requirements and
// bugfix first, then design, then tasks, then everything else.
func Rank(file string) int {
	switch strings.TrimSuffix(path.Base(file), ".md") {
	case "requirements", "bugfix":
		return 0
	case "design":
		return 1
	case "tasks":
		return 2
	default:
		return 3
	}
}

func roleOf(file string) marotte.SpecDocRole {
	switch Rank(file) {
	case 0:
		return marotte.SpecDocRoleRequirements
	case 1:
		return marotte.SpecDocRoleDesign
	case 2:
		return marotte.SpecDocRoleTasks
	default:
		return marotte.SpecDocRoleOther
	}
}

// Load reads the spec named name under root through the root, so a symlink
// out of it is refused by the kernel and a FIFO is refused rather than
// blocked on. Regular *.md files are the documents; one over maxBytes is
// listed with TooLarge and no content; any other entry that cannot be read as
// a regular file inside the root is not a document. A missing directory
// wraps fs.ErrNotExist, and so does an invalid name; a directory with no
// document is ErrNoDocs.
func Load(ctx context.Context, root Root, name string, maxBytes int64) (marotte.Spec, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return marotte.Spec{}, fmt.Errorf("spec: invalid name %q: %w", name, fs.ErrNotExist)
	}
	r, err := os.OpenRoot(root.Dir)
	if err != nil {
		return marotte.Spec{}, err
	}
	defer r.Close()
	dir := "specs/" + name
	entries, err := fs.ReadDir(r.FS(), dir)
	if err != nil {
		return marotte.Spec{}, err
	}
	spec := marotte.Spec{Dir: root.Rel + "/" + dir, Name: name}
	if err := loadDocs(ctx, r, dir, entries, maxBytes, &spec); err != nil {
		return marotte.Spec{}, err
	}
	if len(spec.Docs) == 0 {
		return marotte.Spec{}, ErrNoDocs
	}
	slices.SortStableFunc(spec.Docs, func(a, b marotte.SpecDoc) int {
		return cmp.Or(cmp.Compare(Rank(a.File), Rank(b.File)), strings.Compare(a.File, b.File))
	})
	return spec, nil
}

func loadDocs(ctx context.Context, r *os.Root, dir string, entries []fs.DirEntry, maxBytes int64, spec *marotte.Spec) error {
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		doc, mtime, err := loadDoc(ctx, r, dir+"/"+e.Name(), maxBytes)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			continue
		}
		spec.Docs = append(spec.Docs, doc)
		if mtime.After(spec.UpdatedAt) {
			spec.UpdatedAt = mtime
		}
	}
	return nil
}

func loadDoc(ctx context.Context, r *os.Root, p string, maxBytes int64) (marotte.SpecDoc, time.Time, error) {
	doc := marotte.SpecDoc{File: path.Base(p), Role: roleOf(p)}
	data, err := atomicfile.ReadBoundedInRoot(ctx, r, p, maxBytes)
	switch {
	case errors.Is(err, atomicfile.ErrFileTooLarge):
		doc.TooLarge = true
	case err != nil:
		return marotte.SpecDoc{}, time.Time{}, err
	default:
		sum := sha256.Sum256(data)
		doc.Hash = hex.EncodeToString(sum[:])
		doc.Content = string(data)
		if doc.Role == marotte.SpecDocRoleTasks {
			parsed := Parse(data)
			doc.Tasks = parsed.Tasks
			doc.Progress = &parsed.Progress
			doc.UnreadableLines = parsed.UnreadableLines
			doc.Truncated = parsed.Truncated
		}
	}
	info, err := r.Stat(p)
	if err != nil {
		return marotte.SpecDoc{}, time.Time{}, err
	}
	return doc, info.ModTime().UTC(), nil
}
