// Package obs owns marotte's Prometheus metrics and the registry that serves
// them, with the exposition prefix "marotte_". The registry is unexported, so
// a caller can scrape the page but cannot register, rename or delete a series.
package obs

import (
	"net/http"

	"github.com/cplieger/go/measure"
)

var registry = measure.NewRegistry("marotte")

// Handler serves the Prometheus text exposition: every marotte series plus the
// Go and process families measure adds to every registry. It is the page's
// only writer, because a second writer of those families repeats their TYPE
// lines and Prometheus then rejects the whole scrape.
func Handler() http.Handler {
	return registry.Handler()
}
