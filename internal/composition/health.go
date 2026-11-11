package composition

import (
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/cplieger/go/health/v2/probe"
)

// HealthCommand is the subcommand the image's HEALTHCHECK runs.
const HealthCommand = "health"

// healthTimeout sits below the Dockerfile HEALTHCHECK's --timeout=5s, so a hung server fails with
// the probe's written reason instead of Docker's kill.
const healthTimeout = 4 * time.Second

// RunHealth probes this instance's GET /healthz at LISTEN_ADDR and returns 0 when it answers 2xx,
// 1 otherwise with one line on stderr saying why. It reads no config and takes no lock, so it runs
// beside the server it probes.
func RunHealth(stderr io.Writer) int {
	_, local, err := listenAddrFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "unhealthy: %v\n", err)
		return 1
	}
	return probe.Check(stderr, healthTimeout, (&url.URL{Scheme: "http", Host: local, Path: "/healthz"}).String())
}
