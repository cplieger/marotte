//go:build marotte_test

package composition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/marotte/internal/signin"
)

// qaSignInEnv turns the scripted sign-in on. It is read only in a marotte_test build, so a browser check can fake an
// identity provider outage without reaching AWS or the real sign-in file.
const qaSignInEnv = "MAROTTE_TEST_SIGNIN_STUB"

// qaModeFile, under KIRO_CONFIG_DIR, holds what every renewal returns: ok, transient or rejected.
const qaModeFile = "qa-signin-mode"

// qaAskEvery is how often the scripted engine asks for a credential, as a running kiro-cli does on each request.
const qaAskEvery = 15 * time.Second

// qaLease is the expiry of every record the script issues: servable (over the 210 s floor) and never fresh (under
// the 300 s renew mark), so each ask renews and a mode change shows at the next one.
const qaLease = 250 * time.Second

// qaBackend wraps the kiro backend: a sign-in completes at once with a Builder ID record, and Refresh answers from the
// mode file. Every other method is the kiro backend's, so the vault judges the records as it does in production.
type qaBackend struct {
	signin.Backend
	modePath string
}

func qaSignIn(b signin.Backend) signin.Backend {
	if os.Getenv(qaSignInEnv) == "" {
		return b
	}
	slog.Warn("scripted Kiro sign-in mounted; this is a marotte_test build", "mode_file", qaModeFile)
	return &qaBackend{Backend: b, modePath: filepath.Join(os.Getenv("KIRO_CONFIG_DIR"), qaModeFile)}
}

func (*qaBackend) Begin(context.Context, signin.FlowParams) (signin.DeviceStart, error) {
	return signin.DeviceStart{
		VerificationURL: "https://view.awsapps.com/start/#/device?user_code=QAQA-QAQA",
		UserCode:        "QAQA-QAQA",
		ExpiresIn:       time.Minute,
	}, nil
}

func (*qaBackend) Poll(ctx context.Context, _ signin.DeviceStart) (signin.Credential, error) {
	if !qaWait(ctx, 2*time.Second) {
		return signin.Credential{}, ctx.Err()
	}
	return qaRecord(time.Now())
}

func (b *qaBackend) Refresh(ctx context.Context, _ signin.Credential) (signin.Credential, error) {
	raw, err := os.ReadFile(b.modePath)
	mode := strings.TrimSpace(string(raw))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return signin.Credential{}, fmt.Errorf("qa: read the mode file: %w", err)
	}
	switch mode {
	case "", "ok":
		return qaRecord(time.Now())
	case "transient":
		qaWait(ctx, 500*time.Millisecond)
		return signin.Credential{}, errors.New("qa: the identity provider answered 503")
	case "rejected":
		return signin.Credential{}, fmt.Errorf("%w: qa: invalid_grant", signin.ErrRejected)
	default:
		return signin.Credential{}, fmt.Errorf("qa: unknown mode %q", mode)
	}
}

func qaWait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func qaRecord(now time.Time) (signin.Credential, error) {
	data, err := json.Marshal(map[string]any{
		"clientSecretExpiresAt": now.Add(90 * 24 * time.Hour),
		"expiresAt":             now.Add(qaLease),
		"startUrl":              "https://view.awsapps.com/start",
		"region":                "us-east-1",
		"clientId":              "qa-client",
		"clientSecret":          "qa-secret",
		"refreshToken":          "qa-refresh",
		"accessToken":           "qa-access",
		"provider":              "BuilderId",
		"email":                 "qa@example.com",
	})
	return signin.Credential{Data: data}, err
}

// startQASignIn signs the scripted account in when none is, then asks for a credential every qaAskEvery as an engine
// would, so the vault's renewals, grace and health owner run on the mode file's answers.
func startQASignIn(ctx context.Context, vault *signin.Vault, id signin.BackendID) {
	if os.Getenv(qaSignInEnv) == "" {
		return
	}
	if vault.Status(id).Kind != signin.StatusSignedIn {
		if _, err := vault.StartSignIn(ctx, id, signin.FlowParams{Flow: signin.FlowDevice}); err != nil {
			slog.Error("qa: the scripted sign-in did not start", "error", err)
		}
	}
	go func() {
		tick := time.NewTicker(qaAskEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			if vault.Status(id).Kind == signin.StatusSignedIn {
				_, rerr := vault.Answer(ctx, id)
				slog.Info("qa: scripted credential ask", "refused", rerr != nil)
			}
		}
	}()
}
