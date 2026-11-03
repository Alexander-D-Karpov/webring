package siteurl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	good := map[string]string{
		"site.example.com/blog?x=1":         "https://site.example.com/blog?x=1",
		"HTTPS://SITE.EXAMPLE.COM:443/path": "https://site.example.com/path",
		"http://site.example.org:80/":       "http://site.example.org/",
		"https://site.example.com/#part":    "https://site.example.com/",
	}
	for raw, want := range good {
		t.Run(raw, func(t *testing.T) {
			got, err := Normalize(raw)
			if err != nil || got != want {
				t.Errorf("Normalize(%q) = %q, %v; want %q", raw, got, err, want)
			}
		})
	}
	bad := []string{
		"", "https://singlelabel", "https://com", "https://co.uk", "https://site.invalid",
		"https://localhost", "http://127.0.0.1", "http://[::1]/", "https://user:pass@site.example.com",
		"https://site.example.com:8080", "https://site.example.com:80", "https://-bad.example.com",
		"https://a..com", "https://foo_bar.example.com", "https://site.example.com@127.0.0.1",
		"ftp://site.example.com", "file:///etc/passwd", "https://site.example.com\nother",
	}
	for _, raw := range bad {
		t.Run(raw, func(t *testing.T) {
			if normalized, err := Normalize(raw); err == nil {
				t.Errorf("Normalize(%q) accepted %q", raw, normalized)
			}
		})
	}
}

func TestAddressPolicy(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "100.64.0.1",
		"169.254.169.254", "198.51.100.2", "192.0.2.4", "198.19.0.1", "203.0.113.4",
		"0.0.0.0", "::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "2001:db8::1",
		"64:ff9b::7f00:1", "100::1", "2002::1", "3fff::1",
	}
	for _, raw := range blocked {
		if isPublicIP(net.ParseIP(raw)) {
			t.Errorf("private or special IP accepted: %s", raw)
		}
	}
	// Construct a synthetic globally routable address; it is never contacted.
	if !isPublicIP(net.IPv4(11, 1, 2, 3)) {
		t.Error("globally routable address rejected")
	}
	if ips, err := checkedAddresses([]net.IPAddr{
		{IP: net.IPv4(11, 1, 2, 3)}, {IP: net.ParseIP("169.254.169.254")},
	}); err == nil || ips != nil {
		t.Fatalf("mixed public/private DNS accepted: ips=%v err=%v", ips, err)
	}
}

// fakeNetwork permits only a documentation-range address, and maps that address
// to a local HTTP server. No real DNS server or external website is contacted.
type fakeNetwork struct {
	mu         sync.Mutex
	lookups    []string
	dials      []string
	rebindHost string
	server     *httptest.Server
}

func (f *fakeNetwork) lookup(_ context.Context, host string) ([]net.IP, error) {
	f.mu.Lock()
	f.lookups = append(f.lookups, host)
	rebind := host == f.rebindHost && len(f.lookups) > 1
	f.mu.Unlock()
	if host == "rebound.example.com" || rebind {
		return checkedAddresses([]net.IPAddr{{IP: net.ParseIP("127.0.0.1")}})
	}
	if host == "missing.example.com" {
		return nil, errors.New("no DNS records")
	}
	if !strings.HasSuffix(host, ".example.com") {
		return nil, errors.New("unexpected test host")
	}
	return []net.IP{net.ParseIP("203.0.113.7")}, nil
}

func (f *fakeNetwork) dial(ctx context.Context, network, address string) (net.Conn, error) {
	f.mu.Lock()
	f.dials = append(f.dials, address)
	f.mu.Unlock()
	if address != "203.0.113.7:80" {
		return nil, fmt.Errorf("unexpected test address: %s", address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, f.server.Listener.Addr().String())
}

func newFakeNetwork(t *testing.T) (*fakeNetwork, *http.Client) {
	t.Helper()
	f := &fakeNetwork{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/local":
			http.Redirect(w, r, "http://127.0.0.1/", http.StatusFound)
		case "/port":
			http.Redirect(w, r, "http://site.example.com:8080/", http.StatusFound)
		case "/rebind":
			http.Redirect(w, r, "http://rebound.example.com/ok", http.StatusFound)
		case "/rebind-same":
			http.Redirect(w, r, "http://site.example.com/ok", http.StatusFound)
		case "/redirect":
			http.Redirect(w, r, "http://other.example.com/ok", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	client := newHTTPClient(2*time.Second, f.lookup, f.dial)
	t.Cleanup(client.CloseIdleConnections)
	return f, client
}

func TestVerifyWithFakeNetwork(t *testing.T) {
	f, client := newFakeNetwork(t)
	got, err := verify(context.Background(), "http://site.example.com/ok", client)
	if err != nil || got != "http://site.example.com/ok" {
		t.Fatalf("valid test site: %q, %v", got, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lookups) != 1 || f.lookups[0] != "site.example.com" ||
		len(f.dials) != 1 || f.dials[0] != "203.0.113.7:80" {
		t.Fatalf("DNS was not pinned: lookups=%v dials=%v", f.lookups, f.dials)
	}
}

func TestHTTPSVerifiesHostnameWhenDialingPinnedAddress(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	lookup := func(_ context.Context, _ string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.7")}, nil
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "203.0.113.7:443" {
			return nil, fmt.Errorf("unexpected test address: %s", address)
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	client := newHTTPClient(2*time.Second, lookup, dial)
	t.Cleanup(client.CloseIdleConnections)
	testTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected test server transport")
	}
	safe, ok := client.Transport.(*safeTransport)
	if !ok {
		t.Fatal("unexpected safe client transport")
	}
	safe.transport.TLSClientConfig = testTransport.TLSClientConfig.Clone()
	if _, err := verify(context.Background(), "https://site.example.com/", client); err != nil {
		t.Fatalf("valid test certificate rejected: %v", err)
	}
	if _, err := verify(context.Background(), "https://deep.site.example.com/", client); err == nil {
		t.Fatal("certificate with the wrong hostname was accepted")
	}
}

func TestVerifyRejectsUnsafeResponsesAndRedirects(t *testing.T) {
	f, client := newFakeNetwork(t)
	for _, path := range []string{"/missing", "/local", "/port", "/rebind", "/loop"} {
		t.Run(path, func(t *testing.T) {
			if _, err := verify(context.Background(), "http://site.example.com"+path, client); err == nil {
				t.Fatal("unsafe response or redirect was accepted")
			}
		})
	}
	if _, err := verify(context.Background(), "http://missing.example.com/ok", client); err == nil {
		t.Fatal("missing DNS was accepted")
	}
	if _, err := verify(context.Background(), "http://site.example.com/redirect", client); err != nil {
		t.Fatalf("safe redirect rejected: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, address := range f.dials {
		if address != "203.0.113.7:80" {
			t.Errorf("unexpected dial: %s", address)
		}
	}
}

func TestHTTPClientRejectsUnsafeInitialFaviconURLs(t *testing.T) {
	f, client := newFakeNetwork(t)
	for _, raw := range []string{
		"http://127.0.0.1/", "http://site.example.com:8080/", "http://site.invalid/", "ftp://site.example.com/",
	} {
		res, err := client.Get(raw)
		if res != nil {
			if closeErr := res.Body.Close(); closeErr != nil {
				t.Errorf("closing response: %v", closeErr)
			}
		}
		if err == nil {
			t.Errorf("unsafe initial URL was accepted: %q", raw)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lookups) != 0 || len(f.dials) != 0 {
		t.Fatalf("unsafe URL reached network: lookups=%v dials=%v", f.lookups, f.dials)
	}
}

func TestHTTPClientChecksDNSAgainAfterRedirect(t *testing.T) {
	f, client := newFakeNetwork(t)
	f.rebindHost = "site.example.com"
	if _, err := verify(context.Background(), "http://site.example.com/rebind-same", client); err == nil {
		t.Fatal("DNS rebinding was accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lookups) != 2 || len(f.dials) != 1 {
		t.Fatalf("rebound address was dialed: lookups=%v dials=%v", f.lookups, f.dials)
	}
}

func TestVerifyHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Verify(ctx, "http://site.example.com/"); err == nil {
		t.Fatal("canceled verification succeeded")
	}
}
