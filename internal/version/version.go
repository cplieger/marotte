// Package version holds the build version string, stamped by the release image build.
//
// The -X path must be the FULL module path:
// `-ldflags "-X github.com/cplieger/marotte/internal/version.Build=<tag>"`. The linker
// silently discards a path matching no package, leaving the default.
package version

// Build is the release tag the image build stamps in (e.g. "v0.8.3"). Stays
// "dev" for a plain go build, which is how a developer build identifies itself.
var Build = "dev"
