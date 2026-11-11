package composition

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/cplieger/go/envx"
	"github.com/cplieger/go/health/v2/probe"
)

const (
	listenAddrVar     = "LISTEN_ADDR"
	defaultListenAddr = ":9847"
)

// listenAddrFromEnv returns the server's net.Listen address from LISTEN_ADDR, :9847 when unset,
// and the host:port a process in the container reaches it at. An address the health probe could
// not reach, or reaches on a host other than loopback, is an error naming the variable.
func listenAddrFromEnv() (addr, local string, err error) {
	addr = cmp.Or(envx.String(listenAddrVar), defaultListenAddr)
	u, err := probe.LocalURL(addr, "/")
	if err != nil {
		if ae, ok := errors.AsType[*net.AddrError](err); ok {
			err = errors.New(ae.Err) // its Error() echoes the address unquoted
		}
		return addr, "", fmt.Errorf("%s=%q: %w", listenAddrVar, addr, err)
	}
	parsed, _ := url.Parse(u) // LocalURL returns only a URL url.Parse accepts
	if !loopbackHost(parsed.Hostname()) {
		return addr, "", fmt.Errorf("%s=%q: the host is not empty, 0.0.0.0, ::, localhost or a loopback address, "+
			"which the healthcheck and the in-container API need", listenAddrVar, addr)
	}
	return addr, parsed.Host, nil
}

// loopbackHost reports whether ALLOWED_HOSTS and the loopback-only routes admit a request
// addressed to host from inside the container. A zone is refused: httpserver's Host
// canonicalisation reads no zoned address as loopback.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Zone() == "" && a.IsLoopback()
}
