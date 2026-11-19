package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/cplieger/marotte/internal/marotte"
	"github.com/cplieger/marotte/internal/settings"
)

// kiroAccountSignedIn reports whether a Kiro account, not an API key, signs the engine in with its asks answered: only
// then does KAS hold an account layer a fallback can be written to.
func (rt *Runtime) kiroAccountSignedIn() bool {
	return !rt.signin.apiKey && rt.signInHealth.snapshot().State == marotte.SignInOK
}

// seedModelFallbacksSoon runs seedModelFallbacks off the caller, which is a lane consumer: the write is an engine
// call that consumer answers.
func (st *Settings) seedModelFallbacksSoon() {
	if st.lifecycle.configDir == "" {
		return
	}
	st.lifecycle.goUnlessDraining(func() { st.seedModelFallbacks(st.lifecycle.shutdownCtx) })
}

// seedModelFallbacks writes Kiro's own default as the account's fallback for each model whose row is unset and
// editable, because KAS uses its default only for a declined request and an unavailable model would get none. Each
// model is filled once: a seeded model, or one found with a fallback already chosen, joins
// settings.KeyModelFallbackSeeded and is never filled again, so a row the user clears or changes stays theirs. A
// failed write is not recorded and is tried again at the next view. Nothing runs while Model fallback is off or no
// Kiro account signs the engine in.
func (st *Settings) seedModelFallbacks(ctx context.Context) {
	st.seedMu.Lock()
	defer st.seedMu.Unlock()
	if !st.kiroAccount() {
		return
	}
	dir := st.lifecycle.configDir
	d, _, err := settings.Read(ctx, dir)
	if err != nil || !modelFallbackEnabled(d) {
		return
	}
	done, _, _ := settings.Lookup[[]string](d, settings.KeyModelFallbackSeeded)
	record, wrote := st.fillUnseededFallbacks(ctx, done)
	if len(record) == 0 {
		return
	}
	if err := recordSeededFallbacks(ctx, dir, record, st.broadcast); err != nil {
		slog.Warn("seeded model fallbacks were not recorded", "models", record, "error", err)
		if wrote {
			st.announceSettings()
		}
	}
}

// fillUnseededFallbacks writes Kiro's default to each unset, editable model not in done and returns the models to
// record (filled or found already chosen); wrote says a write landed.
func (st *Settings) fillUnseededFallbacks(ctx context.Context, done []string) (record []string, wrote bool) {
	for _, f := range st.ModelFallbacks() {
		if f.Model == marotte.ModelAuto || slices.Contains(done, f.Model) {
			continue
		}
		switch {
		case f.Configured != "":
			record = append(record, f.Model)
		case f.VendorDefault != "" && f.ReadOnlyReason == "":
			if rep := st.setModelFallback(ctx, f.Model, []string{f.VendorDefault}); rep.err != nil {
				slog.Warn("a model's fallback was not seeded with Kiro's default",
					"model", f.Model, "fallback", f.VendorDefault, "error", rep.err)
				continue
			}
			wrote = true
			record = append(record, f.Model)
		}
	}
	return record, wrote
}

// recordSeededFallbacks adds models to settings.KeyModelFallbackSeeded, sorted; a stored value that does not decode
// is replaced, since no reader could use it.
func recordSeededFallbacks(ctx context.Context, dir string, models []string, publish func(context.Context, marotte.ServerEvent)) error {
	_, err := settings.Update(ctx, dir, func(doc map[string]json.RawMessage) error {
		var stored []string
		if raw, ok := doc[settings.KeyModelFallbackSeeded]; ok && json.Unmarshal(raw, &stored) != nil {
			stored = nil
		}
		for _, m := range models {
			if !slices.Contains(stored, m) {
				stored = append(stored, m)
			}
		}
		slices.Sort(stored)
		raw, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		doc[settings.KeyModelFallbackSeeded] = raw
		return nil
	}, publish)
	return err
}
