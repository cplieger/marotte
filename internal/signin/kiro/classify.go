package kiro

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/cplieger/marotte/internal/signin"
)

const (
	builderIDStartURL = "https://view.awsapps.com/start"
	// amazonStartURL is the one IAM Identity Center portal kiro-cli classes Internal.
	amazonStartURL = "https://amzn.awsapps.com/start"
)

const textStartURLShape = "Enter your organization's AWS access portal URL, which looks like https://your-company.awsapps.com/start."

// canonicalStartURL is the one spelling of the portal a pasted start URL names: https:// added when
// it has no scheme, the fragment (the portal's "#/" included) and the query dropped, the host
// lower-cased, a trailing "/" removed, and /start used for an empty path. A value that is not an
// https URL with a host and no userinfo (CWE-601) is refused with ErrInvalidParams.
func canonicalStartURL(raw string) (string, error) {
	s, _, _ := strings.Cut(strings.TrimSpace(raw), "#")
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	if len(s) > maxStartURLBytes {
		return "", shapeError("the start URL is too long")
	}
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "", shapeError("the start URL must be an https URL")
	}
	if u.User != nil {
		return "", shapeError("the start URL must not contain credentials")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		path = "/start"
	}
	return "https://" + strings.ToLower(u.Host) + path, nil
}

func shapeError(why string) error {
	return &signin.FailureError{Text: textStartURLShape, Err: fmt.Errorf("%w: %s", signin.ErrInvalidParams, why)}
}

// classifyStartURL is the provider class kiro-cli reports for a sign-in through canonical, a
// canonicalStartURL result. It is decided once at sign-in and stored, never recomputed.
func classifyStartURL(canonical string) provider {
	switch canonical {
	case builderIDStartURL:
		return providerBuilderID
	case amazonStartURL:
		return providerInternal
	default:
		return providerEnterprise
	}
}
