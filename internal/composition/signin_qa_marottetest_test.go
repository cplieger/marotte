//go:build marotte_test

package composition

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cplieger/marotte/internal/signin"
	kirosignin "github.com/cplieger/marotte/internal/signin/kiro"
)

func TestQASignIn_RefreshAnswersFromTheModeFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KIRO_CONFIG_DIR", dir)
	t.Setenv(qaSignInEnv, "1")
	kiro := kirosignin.New()
	b := qaSignIn(kiro)
	mode := func(m string) {
		if err := os.WriteFile(filepath.Join(dir, qaModeFile), []byte(m+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()

	t.Run("no file renews", func(t *testing.T) {
		c, err := b.Refresh(ctx, signin.Credential{})
		if err != nil {
			t.Fatalf("Refresh = %v, want a record", err)
		}
		if err := kiro.Validate(c); err != nil {
			t.Errorf("the kiro backend refuses the scripted record: %v", err)
		}
		now := time.Now()
		if kiro.Fresh(c, now) || !kiro.Servable(c, now) {
			t.Errorf("the scripted record must be servable and not fresh, so every ask renews")
		}
	})
	t.Run("transient is not a rejection", func(t *testing.T) {
		mode("transient")
		_, err := b.Refresh(ctx, signin.Credential{})
		if err == nil || errors.Is(err, signin.ErrRejected) {
			t.Errorf("Refresh = %v, want a transient error", err)
		}
	})
	t.Run("rejected wraps ErrRejected", func(t *testing.T) {
		mode("rejected")
		if _, err := b.Refresh(ctx, signin.Credential{}); !errors.Is(err, signin.ErrRejected) {
			t.Errorf("Refresh = %v, want ErrRejected", err)
		}
	})
	t.Run("an unknown mode fails", func(t *testing.T) {
		mode("sideways")
		if _, err := b.Refresh(ctx, signin.Credential{}); err == nil {
			t.Error("Refresh = nil, want an error")
		}
	})
}

func TestQASignIn_OffWithoutTheEnv(t *testing.T) {
	t.Setenv(qaSignInEnv, "")
	kiro := kirosignin.New()
	if got := qaSignIn(kiro); got != signin.Backend(kiro) {
		t.Errorf("qaSignIn wrapped the backend with %s unset", qaSignInEnv)
	}
}
