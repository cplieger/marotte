package kiro

import (
	"net/url"
	"strings"
)

const (
	builderIDStartURL = "https://view.awsapps.com/start"
	// amazonStartURL is the one IAM Identity Center portal kiro-cli classes Internal.
	amazonStartURL = "https://amzn.awsapps.com/start"
)

// classifyStartURL is the provider class kiro-cli reports for a sign-in through startURL: "" is
// Builder ID. It is decided once at sign-in and stored, never recomputed.
func classifyStartURL(startURL string) provider {
	if startURL == "" {
		return providerBuilderID
	}
	switch normalizeStartURL(startURL) {
	case builderIDStartURL:
		return providerBuilderID
	case amazonStartURL:
		return providerInternal
	default:
		return providerEnterprise
	}
}

// normalizeStartURL lower-cases the scheme and host and drops the query, the fragment (the portal's
// "#/" included) and a trailing "/", so every spelling of one portal compares equal. An
// unparseable URL normalizes to "", which matches no known portal.
func normalizeStartURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(u.EscapedPath(), "/")
}
