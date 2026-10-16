package kiro

import (
	"testing"
	"time"

	"github.com/cplieger/runesafe/v3"
)

// FuzzDecodeTokenResponse covers every decoder of a remote answer: an accepted token carries an
// access token and a lifetime inside (0, maxTokenTTL]; an accepted device authorization a usable
// https URL and a displayable code; a kept profile a displayable ARN; an accepted email a
// displayable line of at most 320 bytes.
func FuzzDecodeTokenResponse(f *testing.F) {
	for _, seed := range []string{
		`{"accessToken":"a","refreshToken":"r","expiresIn":3600}`,
		`{"accessToken":"","expiresIn":-1}`,
		`{"accessToken":"a","expiresIn":9223372036854775807}`,
		`{"deviceCode":"d","userCode":"U","verificationUriComplete":"https://x.example/?c=U","expiresIn":600,"interval":5}`,
		`{"deviceCode":"d","userCode":"U\u001b","verificationUri":"javascript:alert(1)"}`,
		`{"profiles":[{"arn":"arn:x","profileName":"n\u202e"},{"arn":""}]}`,
		`{"userInfo":{"email":"a@b.c"}}`,
		`{"userInfo":{"email":"a@b.c\n"}}`,
		`[]`, `null`, `{`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if tok, err := decodeTokenResponse(body); err == nil {
			if tok.AccessToken == "" {
				t.Fatal("accepted a token with no access token")
			}
			if ttl := tok.ttl(); ttl <= 0 || ttl > maxTokenTTL {
				t.Fatalf("ttl %v outside (0, %v]", ttl, maxTokenTTL)
			}
		}
		if d, err := decodeDeviceAuthorization(body); err == nil {
			if validStartURL(d.verificationURL()) != nil || runesafe.SanitizeSingleLineBounded(d.UserCode, 64) != d.UserCode {
				t.Fatalf("accepted an unusable device authorization %+v", d)
			}
		}
		if ps, err := decodeProfiles(body); err == nil {
			for _, p := range ps {
				if p.Arn == "" || !displayable(p.Arn, maxStartURLBytes) || !displayable(p.ProfileName, maxProfileNameBytes) {
					t.Fatalf("kept an undisplayable profile %+v", p)
				}
			}
		}
		if e, ok := decodeEmail(body); ok {
			if e == "" || len(e) > maxEmailBytes || runesafe.SanitizeSingleLineBounded(e, maxEmailBytes) != e {
				t.Fatalf("accepted email %q", e)
			}
			r := testRecord(providerBuilderID, "")
			r.Email = e
			r.ExpiresAt = time.Unix(1, 0)
			if err := validRecord(&r); err != nil {
				t.Fatalf("an accepted email %q fails the record: %v", e, err)
			}
		}
	})
}
