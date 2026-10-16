package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	listProfilesTarget = "AmazonCodeWhispererService.ListAvailableProfiles"
	maxProfiles        = 10
	emailTimeout       = 10 * time.Second
)

// User text for the profile lookup's failures (IAM Identity Center only; Builder ID goes on).
const (
	textNoProfile          = "This account has no Kiro profile marotte can use."
	textProfileUnreachable = "marotte could not reach Kiro to look up this account's profile. Try again."
)

type profile struct {
	Arn         string `json:"arn"`
	ProfileName string `json:"profileName"`
}

// errNoProfile is a control plane that answered with no usable profile.
var errNoProfile = errors.New("kiro: the account has no Kiro profile")

// firstProfile asks the control plane for the account's profiles and returns the first.
func (b *Backend) firstProfile(ctx context.Context, management, accessToken string) (profile, error) {
	body, err := json.Marshal(map[string]int{"maxResults": maxProfiles})
	if err != nil {
		return profile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, management+"/", bytes.NewReader(body))
	if err != nil {
		return profile{}, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", listProfilesTarget)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := b.do(req)
	if err != nil {
		return profile{}, err
	}
	if resp.status != http.StatusOK {
		return profile{}, fmt.Errorf("kiro: the control plane answered %d", resp.status)
	}
	profiles, err := decodeProfiles(resp.body)
	if err != nil {
		return profile{}, err
	}
	if len(profiles) == 0 {
		return profile{}, errNoProfile
	}
	if len(profiles) > 1 {
		slog.Info("sign-in: the account has several Kiro profiles; using the first", "profile", profiles[0].ProfileName, "count", len(profiles))
	}
	return profiles[0], nil
}

// decodeProfiles keeps the entries with a displayable ARN, dropping a name that is not displayable.
func decodeProfiles(body []byte) ([]profile, error) {
	var out struct {
		Profiles []profile `json:"profiles"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("kiro: the profile list: %w", err)
	}
	kept := make([]profile, 0, len(out.Profiles))
	for _, p := range out.Profiles {
		if p.Arn == "" || !displayable(p.Arn, maxStartURLBytes) {
			continue
		}
		if !displayable(p.ProfileName, maxProfileNameBytes) {
			p.ProfileName = ""
		}
		kept = append(kept, p)
	}
	return kept, nil
}

// readEmail asks the control plane for the account's email with the sign-in's own token. Anything
// but a displayable address is "" and one WARN naming the status, never the body.
func (b *Backend) readEmail(ctx context.Context, management, accessToken, profileArn string) string {
	ctx, cancel := context.WithTimeout(ctx, b.emailTimeout)
	defer cancel()
	q := url.Values{"isEmailRequired": {"true"}}
	if profileArn != "" {
		q.Set("profileArn", profileArn)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, management+"/Get-Usage-Limits?"+q.Encode(), http.NoBody)
	if err != nil {
		slog.Warn("sign-in: the account email could not be read", "error", err)
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := b.do(req)
	if err != nil {
		slog.Warn("sign-in: the account email could not be read", "error", err)
		return ""
	}
	if resp.status != http.StatusOK {
		slog.Warn("sign-in: the account email could not be read", "status", resp.status)
		return ""
	}
	email, ok := decodeEmail(resp.body)
	if !ok {
		slog.Warn("sign-in: the account email could not be read", "status", resp.status, "reason", "no displayable email in the answer")
	}
	return email
}

func decodeEmail(body []byte) (string, bool) {
	var out struct {
		UserInfo struct {
			Email string `json:"email"`
		} `json:"userInfo"`
	}
	if json.Unmarshal(body, &out) != nil {
		return "", false
	}
	e := out.UserInfo.Email
	if e == "" || !displayable(e, maxEmailBytes) {
		return "", false
	}
	return e, true
}
