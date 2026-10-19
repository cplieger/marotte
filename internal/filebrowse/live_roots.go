package filebrowse

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
)

// WithLiveRoots mounts, beside the roots New was given, every directory roots returns when a
// request arrives, so a change to the list applies from the next request. A directory that
// cannot be opened then is skipped with a warning.
func WithLiveRoots(roots func(ctx context.Context) []string) Option {
	return func(h *Handler) { h.liveRoots = roots }
}

// scoped serves fn on a copy of h whose mounts add the live roots of this request, each with
// its own os.Root, closed once fn returns; fn must not use the copy after it returns.
func (h *Handler) scoped(fn func(*Handler, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.liveRoots == nil {
			fn(h, w, r)
			return
		}
		var extra []string
		for _, dir := range h.liveRoots(r.Context()) {
			if !slices.ContainsFunc(h.mounts, func(m mount) bool { return m.dir == dir }) {
				extra = append(extra, dir)
			}
		}
		live, errs := openMounts(extra)
		for _, err := range errs {
			slog.Warn("filebrowse: skipping live root", "error", err)
		}
		defer func() {
			for _, m := range live {
				_ = m.root.Close()
			}
		}()
		req := *h
		req.mounts = append(slices.Clone(h.mounts), live...)
		sortMounts(req.mounts)
		fn(&req, w, r)
	}
}
