package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// moduleRoot is the module root as seen from this package's directory, where go test runs.
const moduleRoot = "../../../.."

// TestCheckedInTablesAreCurrent regenerates both tables in memory: a checked-in file that differs was edited by
// hand or generated from another Unicode version.
func TestCheckedInTablesAreCurrent(t *testing.T) {
	table := Table()
	goSrc, err := GoSource(table)
	if err != nil {
		t.Fatalf("GoSource: %v", err)
	}
	for _, f := range []struct {
		path string
		want []byte
	}{{GoPath, goSrc}, {TSPath, TSSource(table)}} {
		got, err := os.ReadFile(filepath.Join(moduleRoot, f.path))
		if err != nil {
			t.Fatalf("read %s (run %s from the module root): %v", f.path, Cmd, err)
		}
		if !bytes.Equal(got, f.want) {
			t.Errorf("%s is stale; run %s from the module root", f.path, Cmd)
		}
	}
}

func TestTable(t *testing.T) {
	table := Table()
	fold := map[rune]rune{}
	for i, p := range table {
		if i > 0 && table[i-1].From >= p.From {
			t.Fatalf("Table is not ascending at %d: U+%04X then U+%04X", i, table[i-1].From, p.From)
		}
		if utf16.RuneLen(p.From) != utf16.RuneLen(p.To) {
			t.Errorf("U+%04X -> U+%04X changes UTF-16 length", p.From, p.To)
		}
		fold[p.From] = p.To
	}
	cases := []struct {
		name     string
		from, to rune
		folds    bool
	}{
		{"e acute", 'é', 'e', true},
		{"c cedilla", 'ç', 'c', true},
		{"u diaeresis macron decomposes twice", 'ǖ', 'u', true},
		{"greek alpha tonos", 'ά', 'α', true},
		{"cyrillic short i", 'й', 'и', true},
		{"uppercase is lowered first, so not a key", 'É', 0, false},
		{"o stroke has no canonical decomposition", 'ø', 0, false},
		{"sharp s has no canonical decomposition", 'ß', 0, false},
		{"l stroke has no canonical decomposition", 'ł', 0, false},
		{"a hangul syllable decomposes to letters, not marks", '한', 0, false},
		{"a ligature decomposes only by compatibility", 'ﬁ', 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			to, ok := fold[c.from]
			if ok != c.folds || (c.folds && to != c.to) {
				t.Errorf("fold[%q] = %q, %v; want %q, %v", c.from, to, ok, c.to, c.folds)
			}
		})
	}
}
