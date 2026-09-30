package client

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/ishwarchandra-dev/onegate/internal/domain"
)

// SSRFGuard constrains where upstream requests may go.
//
// Threat model: provider base URLs are configuration (dashboard/API
// editable). A compromised or careless configuration must not be able
// to point the gateway at cloud metadata endpoints or exotic schemes.
// Enforcement happens in two places:
//
//   - checkURL (build time): scheme allowlist and literal-IP URL
//     screening. Catches the obvious cases with precise errors.
//   - dialer Control hook (dial time): the resolved address is checked
//     right before connect. This is the authoritative check — DNS
//     rebinding and metadata hostnames have already collapsed into the
//     concrete IP the socket is about to reach.
//
// Loopback and RFC1918 ranges are NOT blocked by default: local Ollama
// (127.0.0.1:11434) and LAN vLLM clusters are first-class provider
// deployments for a self-hosted gateway. The blocklist targets the
// ranges that only ever mean "instance metadata" in practice.
type SSRFGuard struct {
	// AllowedSchemes is the URL scheme allowlist (default http, https).
	AllowedSchemes []string

	// blocked are the IP ranges refused at dial time.
	blocked []*net.IPNet

	// netDialer is the guard-configured dialer handed to the transport.
	netDialer *net.Dialer
}

// defaultBlockedCIDRs are ranges that host cloud instance metadata or
// are never legitimate provider endpoints:
//
//	169.254.0.0/16     IPv4 link-local: AWS/GCP/Azure metadata (169.254.169.254),
//	                   AWS ECS credentials (169.254.170.2)
//	100.100.100.200/32 Alibaba Cloud metadata
//	192.0.0.192/32     Oracle Cloud metadata
//	fe80::/10          IPv6 link-local
//	fd00:ec2::254/128  AWS IPv6 metadata
var defaultBlockedCIDRs = []string{
	"169.254.0.0/16",
	"100.100.100.200/32",
	"192.0.0.192/32",
	"fe80::/10",
	"fd00:ec2::254/128",
}

// DefaultSSRFGuard returns the default policy: http/https only, cloud
// metadata ranges blocked.
func DefaultSSRFGuard() *SSRFGuard {
	g, err := NewSSRFGuard(nil, defaultBlockedCIDRs)
	if err != nil {
		// The built-in list is fixed and valid; this cannot happen.
		panic(fmt.Sprintf("client: default SSRF list invalid: %v", err))
	}
	return g
}

// NewSSRFGuard builds a guard. schemes nil -> {http, https};
// extraBlocked appends to the default blocklist (nil keeps defaults).
func NewSSRFGuard(schemes, extraBlocked []string) (*SSRFGuard, error) {
	if len(schemes) == 0 {
		schemes = []string{"http", "https"}
	}
	cidrs := append([]string{}, defaultBlockedCIDRs...)
	cidrs = append(cidrs, extraBlocked...)

	g := &SSRFGuard{AllowedSchemes: schemes}
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("client: bad blocked CIDR %q: %w", c, err)
		}
		g.blocked = append(g.blocked, n)
	}
	g.netDialer = &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   g.control,
	}
	return g, nil
}

// checkURL enforces the build-time policy: scheme allowlist plus
// literal-IP screening (dial-time enforcement is the backstop).
func (g *SSRFGuard) checkURL(u *url.URL) error {
	scheme := strings.ToLower(u.Scheme)
	allowed := false
	for _, s := range g.AllowedSchemes {
		if scheme == s {
			allowed = true
			break
		}
	}
	if !allowed {
		return &Error{
			GErr: domain.GatewayError{
				Status: http.StatusBadGateway,
				Type:   domain.ErrPermission,
				Message: fmt.Sprintf("provider URL scheme %q not allowed (allowed: %v)",
					u.Scheme, g.AllowedSchemes),
			},
		}
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if c, reason := g.blockedReason(ip); c != nil {
			return &Error{
				GErr: domain.GatewayError{
					Status:  http.StatusBadGateway,
					Type:    domain.ErrPermission,
					Message: fmt.Sprintf("provider host %s is blocked (%s)", host, reason),
				},
			}
		}
	}
	return nil
}

// control is the dial-time hook: it receives the address the socket is
// about to connect to (post-DNS) and refuses blocked ranges.
func (g *SSRFGuard) control(network, address string, _ syscall.RawConn) error {
	return g.checkAddress(network, address)
}

// checkAddress extracts the IP from a dial address ("ip:port" for TCP,
// zone handling for IPv6) and rejects blocked ranges.
func (g *SSRFGuard) checkAddress(network, address string) error {
	if !strings.HasPrefix(network, "tcp") {
		return nil // only screen TCP connects
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Non-IP dial target after resolution should not happen; if it
		// does, the transport already resolved it. Refuse unknowns.
		return fmt.Errorf("client: ssrf guard: unparseable dial address %q", address)
	}
	if c, reason := g.blockedReason(ip); c != nil {
		return fmt.Errorf("client: ssrf guard: %s is blocked (%s)", ip, reason)
	}
	return nil
}

// blockedReason returns the matching blocked network and a short reason.
func (g *SSRFGuard) blockedReason(ip net.IP) (*net.IPNet, string) {
	for _, n := range g.blocked {
		if n.Contains(ip) {
			return n, "cloud metadata / link-local range"
		}
	}
	return nil, ""
}

// dialer returns the guarded dialer for the transport.
func (g *SSRFGuard) dialer() *net.Dialer { return g.netDialer }
