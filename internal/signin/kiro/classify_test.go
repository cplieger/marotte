package kiro

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/cplieger/marotte/internal/signin"
)

func TestCanonicalStartURL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"acme.awsapps.com", "https://acme.awsapps.com/start"},
		{"https://acme.awsapps.com", "https://acme.awsapps.com/start"},
		{"https://acme.awsapps.com/", "https://acme.awsapps.com/start"},
		{"  acme.awsapps.com/#/  ", "https://acme.awsapps.com/start"},
		{"https://ACME.awsapps.com/start/", "https://acme.awsapps.com/start"},
		{"HTTPS://acme.awsapps.com/start", "https://acme.awsapps.com/start"},
		{"https://d-1.awsapps.com/start/#/?tab=accounts", "https://d-1.awsapps.com/start"},
		{"https://x.example/portal/?a=1", "https://x.example/portal"},
		{"https://amzn.awsapps.com/Start", "https://amzn.awsapps.com/Start"},
	} {
		got, err := canonicalStartURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("canonicalStartURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{
		"http://x.example",
		"https://u:p@x.example/start",
		"https://@x.example/start",
		"https:///start",
		"   ",
		"https://x.example/" + strings.Repeat("a", maxStartURLBytes),
	} {
		_, err := canonicalStartURL(in)
		fe, ok := errors.AsType[*signin.FailureError](err)
		if !errors.Is(err, signin.ErrInvalidParams) || !ok || fe.Text != textStartURLShape {
			t.Errorf("canonicalStartURL(%.40q) = %v, want ErrInvalidParams with the start URL advice", in, err)
		}
	}
}

func TestProvider_ClassifiesLikeKiroCLI(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want provider
	}{
		{"https://view.awsapps.com/start", providerBuilderID},
		{"https://view.awsapps.com", providerBuilderID},
		{"https://amzn.awsapps.com/start", providerInternal},
		{"https://amzn.awsapps.com/start/", providerInternal},
		{"https://amzn.awsapps.com/start#/", providerInternal},
		{"https://amzn.awsapps.com/start?x=1", providerInternal},
		{"HTTPS://AMZN.awsapps.com/start", providerInternal},
		{"amzn.awsapps.com", providerInternal},
		{"https://amzn.awsapps.com", providerInternal},
		{"https://amzn.awsapps.com/start2", providerEnterprise},
		{"https://amzn.awsapps.com/Start", providerEnterprise},
		{"https://evil.example/amzn.awsapps.com/start", providerEnterprise},
		{"https://amzn.awsapps.com.evil.example/start", providerEnterprise},
		{"https://d-1234.awsapps.com/start", providerEnterprise},
		{"https://amzn.awsapps.com:8443/start", providerEnterprise},
	} {
		canonical, err := canonicalStartURL(tc.url)
		if err != nil {
			t.Errorf("canonicalStartURL(%q): %v", tc.url, err)
			continue
		}
		if got := classifyStartURL(canonical); got != tc.want {
			t.Errorf("classifyStartURL(%q) = %s, want %s", canonical, got, tc.want)
		}
	}
}

// TestClassifier_MatchesKiroCLIOnThisAccount compares the classifier with kiro-cli on the account
// the binary named by MAROTTE_LIVE_KIRO_CLI is signed in to. It decodes only the provider member of
// get-kas-token's output and never prints that output, which carries a bearer token.
func TestClassifier_MatchesKiroCLIOnThisAccount(t *testing.T) {
	bin := os.Getenv("MAROTTE_LIVE_KIRO_CLI")
	if bin == "" {
		t.Skip("MAROTTE_LIVE_KIRO_CLI names no kiro-cli binary")
	}
	whoami, err := exec.CommandContext(t.Context(), bin, "whoami", "--format", "json").Output()
	if err != nil {
		t.Fatalf("kiro-cli whoami failed: %v", err)
	}
	var who struct {
		StartURL  string `json:"startUrl"`
		StartURL2 string `json:"start_url"`
	}
	if err := json.NewDecoder(bytes.NewReader(whoami)).Decode(&who); err != nil {
		t.Fatalf("whoami's first value is not a JSON object: %v", err)
	}
	startURL := who.StartURL
	if startURL == "" {
		startURL = who.StartURL2
	}
	tok, err := exec.CommandContext(t.Context(), bin, "chat", "_", "get-kas-token").Output()
	if err != nil {
		t.Fatal("kiro-cli chat _ get-kas-token failed")
	}
	var kas struct {
		Data struct {
			Provider string `json:"provider"`
		} `json:"data"`
	}
	if json.Unmarshal(tok, &kas) != nil || kas.Data.Provider == "" {
		t.Fatal("get-kas-token's output has no data.provider")
	}
	got := providerBuilderID
	if startURL != "" {
		canonical, err := canonicalStartURL(startURL)
		if err != nil {
			t.Fatalf("canonicalStartURL(%q): %v", startURL, err)
		}
		got = classifyStartURL(canonical)
	}
	if string(got) != kas.Data.Provider {
		t.Errorf("the start URL %q classifies as %s, kiro-cli says %s", startURL, got, kas.Data.Provider)
	}
}
