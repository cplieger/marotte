package steering

import (
	"io/fs"
	"strings"
)

// The ONE prefer-`.md` rule for an `agents/` listing, shared by the Kiro docs scan and the
// environment.md generator. The cap and the read stay with each door.

// AgentFile is one agent as an `agents/` listing resolves it.
type AgentFile struct {
	// Base is the agent's name: the filename with its extension stripped, which
	// is what both spellings of a pair share.
	Base string
	// File is the basename of the file chosen to represent it.
	File string
}

// DedupeAgentFiles collapses an `agents/` listing to one file per agent, preferring the `.md`
// (it carries the front-matter) over the `.json`. stat follows a link named by its entry name;
// a link counts only when its target is a regular file. Entries keep first-seen order, so a cap
// at the call site takes a stable prefix.
func DedupeAgentFiles(entries []fs.DirEntry, stat func(name string) (fs.FileInfo, error)) []AgentFile {
	chosen := make(map[string]int, len(entries)) // base name -> index in out
	out := make([]AgentFile, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// A dot-prefix also covers a bare ".md" / ".json", whose base would be empty. KAS reads an
		// agent from a file, so a fifo or a dangling link must not shadow a readable twin.
		if !isAgentFile(e, stat) || strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) {
			continue
		}
		base, ok := agentBaseName(name)
		if !ok {
			continue
		}
		i, seen := chosen[base]
		switch {
		case !seen:
			chosen[base] = len(out)
			out = append(out, AgentFile{Base: base, File: name})
		case strings.HasSuffix(name, ".md") && strings.HasSuffix(out[i].File, ".json"):
			out[i].File = name
		}
	}
	return out
}

func isAgentFile(e fs.DirEntry, stat func(name string) (fs.FileInfo, error)) bool {
	if e.Type()&fs.ModeSymlink == 0 {
		return e.Type().IsRegular()
	}
	fi, err := stat(e.Name())
	return err == nil && fi.Mode().IsRegular()
}

func agentBaseName(filename string) (string, bool) {
	switch {
	case strings.HasSuffix(filename, ".md"):
		return strings.TrimSuffix(filename, ".md"), true
	case strings.HasSuffix(filename, ".json"):
		return strings.TrimSuffix(filename, ".json"), true
	default:
		return "", false
	}
}
