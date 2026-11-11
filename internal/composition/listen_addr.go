package composition

import (
	"cmp"
	"fmt"
	"net/url"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/go/health/v2/probe"
)

const (
	listenAddrVar     = "LISTEN_ADDR"
	defaultListenAddr = ":9847"
)

// listenAddrFromEnv returns the server's net.Listen address from LISTEN_ADDR, :9847 when unset,
// and the host:port a process in the container reaches it at. An address the health probe could
// not reach is an error naming the variable.
func listenAddrFromEnv() (addr, local string, err error) {
	addr = cmp.Or(envx.String(listenAddrVar), defaultListenAddr)
	u, err := probe.LocalURL(addr, "/")
	if err != nil {
		return addr, "", fmt.Errorf("%s: %w", listenAddrVar, err)
	}
	parsed, _ := url.Parse(u) // LocalURL returns only a URL url.Parse accepts
	return addr, parsed.Host, nil
}
