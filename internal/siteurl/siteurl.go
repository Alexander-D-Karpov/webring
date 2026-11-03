// Package siteurl validates public website URLs and confines outbound requests to
// public addresses, including after redirects and DNS changes.
package siteurl

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const (
	maxURLLength  = 500
	maxRedirects  = 5
	verifyTimeout = 10 * time.Second
)

var verifySlots = make(chan struct{}, 8)

var forbiddenNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

// Normalize validates and canonicalizes a public domain URL without contacting it.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLength {
		return "", errors.New("URL is empty or too long")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Host == "" {
		return "", errors.New("URL must have a valid public hostname")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("only HTTP and HTTPS websites are accepted")
	}
	port := u.Port()
	if port != "" && (u.Scheme != "https" || port != "443") && (u.Scheme != "http" || port != "80") {
		return "", errors.New("only default HTTP/HTTPS ports are allowed")
	}
	host, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."))
	if err != nil || !validHostname(host) || net.ParseIP(host) != nil {
		return "", errors.New("URL must use a valid DNS name, not an IP address")
	}
	if !strings.Contains(host, ".") {
		return "", errors.New("a website needs a domain below a public TLD")
	}
	tld := host[strings.LastIndexByte(host, '.')+1:]
	if _, icann := publicsuffix.PublicSuffix(tld); !icann {
		return "", errors.New("unknown or non-public top-level domain")
	}
	if _, err = publicsuffix.EffectiveTLDPlusOne(host); err != nil {
		return "", errors.New("a public suffix alone is not an acceptable site domain")
	}
	u.Host = host
	u.Fragment, u.RawFragment = "", ""
	if len(u.String()) > maxURLLength {
		return "", errors.New("URL is too long")
	}
	return u.String(), nil
}

func validHostname(host string) bool {
	if len(host) < 1 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func isPublicIP(raw net.IP) bool {
	ip, ok := netip.AddrFromSlice(raw)
	if !ok {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Block non-global IPv6 ranges and transition mechanisms such as NAT64.
	if ip.Is6() && !ipv6GlobalUnicast.Contains(ip) {
		return false
	}
	for _, prefix := range forbiddenNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func publicAddresses(ctx context.Context, host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	answers, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(answers) == 0 {
		return nil, errors.New("domain has no DNS A/AAAA records")
	}
	return checkedAddresses(answers)
}

func checkedAddresses(answers []net.IPAddr) ([]net.IP, error) {
	if len(answers) == 0 {
		return nil, errors.New("domain has no DNS A/AAAA records")
	}
	ips := make([]net.IP, 0, len(answers))
	for _, answer := range answers {
		// Do not allow a mixed public/private answer to choose an internal address.
		if !isPublicIP(answer.IP) {
			return nil, errors.New("domain resolves to a non-public IP address")
		}
		ips = append(ips, answer.IP)
	}
	return ips, nil
}

type resolver func(context.Context, string) ([]net.IP, error)
type dialFunc func(context.Context, string, string) (net.Conn, error)

// safeTransport validates the initial request as well as every redirect. This is
// also needed for absolute favicon links, not just the original submitted URL.
type safeTransport struct{ transport *http.Transport }

func (t *safeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := Normalize(req.URL.String()); err != nil {
		return nil, err
	}
	return t.transport.RoundTrip(req)
}

func (t *safeTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }

func newHTTPClient(timeout time.Duration, lookup resolver, dial dialFunc) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   4 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if port != "80" && port != "443" {
				return nil, errors.New("only public web ports are allowed")
			}
			ips, err := lookup(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, errors.New("domain has no DNS A/AAAA records")
			}
			var last error
			for _, ip := range ips {
				conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				last = dialErr
			}
			return nil, last
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &safeTransport{transport: transport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("too many redirects")
			}
			_, err := Normalize(req.URL.String())
			return err
		},
	}
}

// HTTPClient does not use proxies and dials only DNS addresses checked immediately
// before each connection. Redirects and the first request follow the same policy.
func HTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return newHTTPClient(timeout, publicAddresses, dialer.DialContext)
}

// Verify requires a valid domain, public DNS answers and a successful HTTP response.
func Verify(ctx context.Context, raw string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	select {
	case verifySlots <- struct{}{}:
		defer func() { <-verifySlots }()
	case <-ctx.Done():
		return "", errors.New("website validation timed out")
	}
	client := HTTPClient(verifyTimeout)
	defer client.CloseIdleConnections()
	return verify(ctx, raw, client)
}

func verify(ctx context.Context, raw string, client *http.Client) (string, error) {
	normalized, err := Normalize(raw)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized, http.NoBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "webring-validator")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("website is not reachable: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Error closing site verification response: %v", closeErr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("website returned HTTP %d", resp.StatusCode)
	}
	return normalized, nil
}
