package bridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/cplieger/marotte/internal/marotte"
)

const (
	supervisedFile     = "draft.txt"
	supervisedOriginal = "The first draft stays.\n"
	// supervisedNote stands in for the unattended floor's rejection note; KAS keeps whatever text it is given.
	supervisedNote = "This turn started with no one watching it, so the permission was refused automatically."
)

// refuseTurnReview answers a turn review reject_once with supervisedNote and every other ask allow_once, as the
// unattended floor answers a supervised Bot turn nobody watched with the switch off.
func refuseTurnReview(reviews *atomic.Int32) func(json.RawMessage) any {
	return func(raw json.RawMessage) any {
		var p struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
			Meta struct {
				Kiro struct {
					Type string `json:"type"`
				} `json:"kiro"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(raw, &p)
		want := "allow_once"
		if p.Meta.Kiro.Type == "turn_approval" {
			reviews.Add(1)
			want = "reject_once"
		}
		for _, o := range p.Options {
			if o.Kind != want {
				continue
			}
			if want == "reject_once" {
				return marotte.PermissionOutcomeWithRejectionReason(o.OptionID, supervisedNote)
			}
			return marotte.PermissionOutcomeSelected(o.OptionID)
		}
		return marotte.PermissionOutcomeCancelled()
	}
}

func runSupervisedRefused(ctx context.Context, c *capture) error {
	path := filepath.Join(c.work, supervisedFile)
	if err := os.WriteFile(path, []byte(supervisedOriginal), 0o600); err != nil {
		return err
	}
	e, err := c.startEngine(ctx, marotte.EngineFeatures{})
	if err != nil {
		return err
	}
	lane, attach := c.attach()
	c.out(lane, map[string]any{"method": marotte.MethodSessionNew, "params": map[string]any{"cwd": c.work}})
	s, err := e.NewSession(ctx, &marotte.SessionOpts{Role: marotte.SessionRoleChat, Supervised: true}, attach)
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	if !s.SupervisedApplied() {
		return errors.New("the session did not take autopilot off")
	}
	var reviews atomic.Int32
	c.mu.Lock()
	c.permAnswer = refuseTurnReview(&reviews)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.permAnswer = nil
		c.mu.Unlock()
	}()
	if err := c.prompt(ctx, s, lane, "Replace the whole content of "+supervisedFile+
		" in the workspace with the single line: The second draft wins. Use your file-writing tool."); err != nil {
		return err
	}
	if reviews.Load() == 0 {
		return errors.New("the supervised turn asked for no review")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s after the refused review: %w", supervisedFile, err)
	}
	if !bytes.Equal(got, []byte(supervisedOriginal)) {
		return fmt.Errorf("%s after the refused review = %q, want the original %q restored", supervisedFile, got, supervisedOriginal)
	}
	return nil
}
