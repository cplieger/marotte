package bridge_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catalogLine is one recorded frame offering the model select with the given option objects.
func catalogLine(options ...string) string {
	return `{"dir":"in","frame":{"result":{"configOptions":[{"id":"model","currentValue":"auto","options":[` +
		`{"value":"auto","name":"Auto","description":"Models chosen by task"},` + strings.Join(options, ",") + `]}]}}}`
}

const (
	acmeOption   = `{"value":"acme-large-2","name":"Acme Large 2","description":"[Internal] Acme Large 2"}`
	zenithOption = `{"value":"zenith-3.1","name":"Zenith 3.1","description":"Zenith 3.1 [Legacy]"}`
	// orbitOption's second id word and its description's vendor word appear nowhere else.
	orbitOption = `{"value":"acme-orbit-2","name":"Acme Orbit 2","description":"QuillSoft Acme Orbit 2"}`
	// mkOption's id has no word of three letters and its name only short or generic ones, so only
	// the whole id and the whole name can refuse a mention of it.
	mkOption = `{"value":"x9-2","name":"Mk 4 Beta","description":"Mk 4 Beta"}`
)

func TestScrubModels_rewrites(t *testing.T) {
	scrubbedOrbit := catalogLine(`{"value":"model-a","name":"Model A","description":"Model A"}`) + "\n"
	cases := []struct {
		name, in, want string
	}{
		{
			name: "stable letters across repeated catalogs with tags kept",
			in: catalogLine(acmeOption, zenithOption) + "\n" +
				catalogLine(zenithOption, acmeOption) + "\n" +
				`{"dir":"out","frame":{"params":{"configId":"model","value":"zenith-3.1"}}}` + "\n",
			want: catalogLine(
				`{"value":"model-a","name":"Model A","description":"[Internal] Model A"}`,
				`{"value":"model-b","name":"Model B","description":"[Legacy] Model B"}`,
			) + "\n" +
				catalogLine(
					`{"value":"model-b","name":"Model B","description":"[Legacy] Model B"}`,
					`{"value":"model-a","name":"Model A","description":"[Internal] Model A"}`,
				) + "\n" +
				`{"dir":"out","frame":{"params":{"configId":"model","value":"model-b"}}}` + "\n",
		},
		{
			name: "an id that is also the display name stays an id",
			in: catalogLine(`{"value":"acme-large-2","name":"acme-large-2","description":"Acme Large 2 model"}`) + "\n" +
				`{"dir":"out","frame":{"params":{"configId":"model","value":"acme-large-2"}}}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"model-a","description":"Model A"}`) + "\n" +
				`{"dir":"out","frame":{"params":{"configId":"model","value":"model-a"}}}` + "\n",
		},
		{
			name: "escaped spelling of a value is replaced whole",
			in: catalogLine(`{"value":"acme-large-2","name":"Acme \u0026 Large","description":"Acme \u0026 Large"}`) + "\n" +
				`{"text":"\u0061cme-large-2"}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"Model A","description":"Model A"}`) + "\n" + `{"text":"model-a"}` + "\n",
		},
		{
			name: "a string holding an id with a letter after it is left whole",
			in:   catalogLine(mkOption) + "\n" + `{"text":"x9-2b"}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"Model A","description":"Model A"}`) + "\n" + `{"text":"x9-2b"}` + "\n",
		},
		{
			name: "empty description leaves other empty strings alone",
			in:   catalogLine(`{"value":"acme-large-2","name":"Acme Large 2","description":""}`) + "\n" + `{"note":""}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"Model A","description":""}`) + "\n" + `{"note":""}` + "\n",
		},
		{
			name: "missing description leaves other empty strings alone",
			in:   catalogLine(`{"value":"acme-large-2","name":"Acme Large 2"}`) + "\n" + `{"note":""}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"Model A"}`) + "\n" + `{"note":""}` + "\n",
		},
		{
			name: "empty name leaves other empty strings alone",
			in:   catalogLine(`{"value":"acme-large-2","name":"","description":"Acme Large 2"}`) + "\n" + `{"note":""}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"","description":"Model A"}`) + "\n" + `{"note":""}` + "\n",
		},
		{
			name: "placeholder mention passes",
			in:   catalogLine(acmeOption) + "\n" + `{"text":"model-a answered"}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"Model A","description":"[Internal] Model A"}`) + "\n" +
				`{"text":"model-a answered"}` + "\n",
		},
	}
	for _, text := range []string{
		"the pacmen ran", "orbital mechanics", "quillsofts", "the next coder", "a beta feature", "a preview build",
	} {
		line := `{"text":"` + text + `"}` + "\n"
		cases = append(cases, struct{ name, in, want string }{
			name: "a catalog word inside a longer word or a generic word passes: " + text,
			in:   catalogLine(`{"value":"acme-orbit-coder-next","name":"Acme Orbit Beta Preview","description":"QuillSoft Acme Orbit"}`) + "\n" + line,
			want: scrubbedOrbit + line,
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scrubModels([]byte(tc.in))
			if err != nil {
				t.Fatalf("scrubModels: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("scrubModels =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestScrubModels_refuses(t *testing.T) {
	cases := []struct{ name, option, text string }{
		{"whole id", mkOption, `switched to X9-2`},
		{"whole display name", mkOption, `asked MK 4 BETA again`},
		{"leading id word", orbitOption, `the acme reply was the placeholder`},
		{"later id word on its own", orbitOption, `Orbit answered`},
		{"id word with a digit after it", orbitOption, `Acme2 wrote it`},
		{"vendor word from the description", orbitOption, `QuillSoft published a note`},
		{"vendor word in another case", orbitOption, `quillsoft published a note`},
		{"id word after a JSON escape", orbitOption, `first line\norbit said`},
		{"id word in a path", orbitOption, `/work/orbit.txt`},
		{"id word in a key", orbitOption, `x","orbit":"y`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := catalogLine(tc.option) + "\n" + `{"text":"` + tc.text + `"}` + "\n"
			if out, err := scrubModels([]byte(in)); err == nil {
				t.Errorf("scrubModels returned no error, wrote\n%s", out)
			}
		})
	}
}

func TestModelLetters(t *testing.T) {
	for i, want := range map[int]string{0: "a", 1: "b", 25: "z", 26: "aa", 27: "ab", 701: "zz", 702: "aaa"} {
		if got := modelLetters(i); got != want {
			t.Errorf("modelLetters(%d) = %q, want %q", i, got, want)
		}
	}
}

// TestScrubModels_corpusIsScrubbed pins the committed corpus as already scrubbed: a second pass
// changes nothing and refuses nothing.
func TestScrubModels_corpusIsScrubbed(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(corpusDir, "*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob %s: %v, %d files", corpusDir, err, len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			in, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			out, err := scrubModels(in)
			if err != nil {
				t.Fatalf("scrubModels: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Error("scrubModels changed the committed file")
			}
		})
	}
}
