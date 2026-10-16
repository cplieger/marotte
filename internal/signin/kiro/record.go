package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cplieger/marotte/internal/signin"
	"github.com/cplieger/runesafe/v3"
)

// provider is the token_provider class KAS reads governance from: Enterprise is
// organization-governed, BuilderId and Internal are not.
type provider string

const (
	providerBuilderID  provider = "BuilderId"
	providerInternal   provider = "Internal"
	providerEnterprise provider = "Enterprise"
)

const (
	maxStartURLBytes    = 2048
	maxRegionBytes      = 32
	maxEmailBytes       = 320
	maxProfileNameBytes = 256
)

// record is the kiro backend's credential, the Data of a signin.Credential. Every record that exists
// passed validRecord, so its fields are safe to display and its provider and profile agree.
type record struct {
	ClientSecretExpiresAt time.Time `json:"clientSecretExpiresAt"`
	ExpiresAt             time.Time `json:"expiresAt"`
	StartURL              string    `json:"startUrl"`
	Region                string    `json:"region"`
	ClientID              string    `json:"clientId"`
	ClientSecret          string    `json:"clientSecret"`
	RefreshToken          string    `json:"refreshToken"`
	AccessToken           string    `json:"accessToken"`
	ProfileArn            string    `json:"profileArn,omitempty"`
	ProfileName           string    `json:"profileName,omitempty"`
	Provider              provider  `json:"provider"`
	Email                 string    `json:"email,omitempty"`
}

// validRecord refuses a record KAS could not use or a UI could not show. An IAM Identity Center class
// with no profile ARN is refused because KAS fails every model call without one; a Builder ID
// record may hold none. No error names a secret.
func validRecord(r *record) error {
	switch r.Provider {
	case providerBuilderID:
	case providerInternal, providerEnterprise:
		if r.ProfileArn == "" {
			return fmt.Errorf("kiro: a %s record needs a profile ARN", r.Provider)
		}
	default:
		return fmt.Errorf("kiro: unknown provider %q", runesafe.SanitizeSingleLineBounded(string(r.Provider), 32))
	}
	if err := validRegion(r.Region); err != nil {
		return err
	}
	if err := validStartURL(r.StartURL); err != nil {
		return err
	}
	if r.ClientID == "" || r.ClientSecret == "" || r.RefreshToken == "" || r.AccessToken == "" {
		return errors.New("kiro: the record is missing a client or token field")
	}
	if r.ExpiresAt.IsZero() {
		return errors.New("kiro: the record has no access expiry")
	}
	if !displayable(r.Email, maxEmailBytes) {
		return errors.New("kiro: the record's email is not a single displayable line")
	}
	if !displayable(r.ProfileName, maxProfileNameBytes) || !displayable(r.ProfileArn, maxStartURLBytes) {
		return errors.New("kiro: the record's profile is not displayable")
	}
	return nil
}

// displayable reports whether s survives single-line sanitizing unchanged within n bytes.
func displayable(s string, n int) bool {
	return len(s) <= n && runesafe.SanitizeSingleLineBounded(s, n) == s
}

// validStartURL accepts an https URL with a host and no userinfo, which some HTTP clients would
// send before a redirect (CWE-601).
func validStartURL(raw string) error {
	if len(raw) > maxStartURLBytes {
		return fmt.Errorf("%w: the start URL is too long", signin.ErrInvalidParams)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: the start URL must be an https URL", signin.ErrInvalidParams)
	}
	if u.User != nil {
		return fmt.Errorf("%w: the start URL must not contain credentials", signin.ErrInvalidParams)
	}
	return nil
}

func decodeRecord(c signin.Credential) (record, error) {
	var r record
	if err := json.Unmarshal(c.Data, &r); err != nil {
		return record{}, fmt.Errorf("kiro: the record is not valid JSON: %w", err)
	}
	if err := validRecord(&r); err != nil {
		return record{}, err
	}
	return r, nil
}

func (r *record) credential() (signin.Credential, error) {
	if err := validRecord(r); err != nil {
		return signin.Credential{}, err
	}
	data, err := json.Marshal(r) //nolint:gosec // G117: the record is the credential the vault stores at 0600
	if err != nil {
		return signin.Credential{}, err
	}
	return signin.Credential{Data: data}, nil
}
