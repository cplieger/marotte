package kiro

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestProvider_ClassifiesLikeKiroCLI(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want provider
	}{
		{"", providerBuilderID},
		{"https://view.awsapps.com/start", providerBuilderID},
		{"https://amzn.awsapps.com/start", providerInternal},
		{"https://amzn.awsapps.com/start/", providerInternal},
		{"https://amzn.awsapps.com/start#/", providerInternal},
		{"https://amzn.awsapps.com/start?x=1", providerInternal},
		{"HTTPS://AMZN.awsapps.com/start", providerInternal},
		{"https://amzn.awsapps.com/start2", providerEnterprise},
		{"https://amzn.awsapps.com/Start", providerEnterprise},
		{"https://evil.example/amzn.awsapps.com/start", providerEnterprise},
		{"https://amzn.awsapps.com.evil.example/start", providerEnterprise},
		{"https://d-1234.awsapps.com/start", providerEnterprise},
		{"https://amzn.awsapps.com:8443/start", providerEnterprise},
	} {
		if got := classifyStartURL(tc.url); got != tc.want {
			t.Errorf("classifyStartURL(%q) = %s, want %s", tc.url, got, tc.want)
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
	if got := classifyStartURL(startURL); string(got) != kas.Data.Provider {
		t.Errorf("classifyStartURL(%q) = %s, kiro-cli says %s", startURL, got, kas.Data.Provider)
	}
}
