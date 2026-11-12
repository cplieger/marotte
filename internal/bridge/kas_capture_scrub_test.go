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
)

func TestScrubModels_rewrites(t *testing.T) {
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
		{
			name: "family letters inside a longer word pass",
			in:   catalogLine(acmeOption) + "\n" + `{"text":"the pacmen ran"}` + "\n",
			want: catalogLine(`{"value":"model-a","name":"Model A","description":"[Internal] Model A"}`) + "\n" +
				`{"text":"the pacmen ran"}` + "\n",
		},
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
	cases := []struct{ name, text string }{
		{"family word in prose", `the acme reply was the placeholder`},
		{"family word in another case", `ACME answered`},
		{"display name in another case", `asked acme large 2 again`},
		{"id in another case", `switched to ZENITH-3.1`},
		{"family word after a JSON escape", `first line\nacme said`},
		{"family word in a path", `/work/zenith.txt`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := catalogLine(acmeOption, zenithOption) + "\n" + `{"text":"` + tc.text + `"}` + "\n"
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
