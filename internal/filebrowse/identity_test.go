package filebrowse

import (
	"strings"
	"testing"
)

func TestFileID_FormatterAndParserAgree(t *testing.T) {
	id := fileIDOf([]byte("hello\n"))
	if got, err := parseFileID(id); err != nil || got != id {
		t.Errorf("parseFileID(fileIDOf(...)) = %q, %v; want %q", got, err, id)
	}
	if etagOf(id) != `"`+id+`"` {
		t.Errorf("etagOf(%q) = %s, want the quoted identity", id, etagOf(id))
	}
}

func TestParseFileID_RefusesEveryOtherShape(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	for _, bad := range []string{
		"", "sha256:", "sha256:" + hex64[:63], "sha256:" + hex64 + "0",
		"sha256:" + strings.Repeat("A", 64), "SHA256:" + hex64, hex64, "stat:3:-1:7",
	} {
		if _, err := parseFileID(bad); err == nil {
			t.Errorf("parseFileID(%q) accepted, want refused", bad)
		}
	}
}
