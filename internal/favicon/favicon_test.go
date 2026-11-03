package favicon

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFaviconCannotContactLoopback(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	icon, err := url.Parse(server.URL + "/icon.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := downloadFavicon(icon, icon, t.TempDir(), 1); err == nil {
		t.Fatal("favicon download accepted loopback")
	}
	if _, err := GetAndStoreFavicon(server.URL, t.TempDir(), 1); err == nil {
		t.Fatal("favicon discovery accepted loopback")
	}
	if hits.Load() != 0 {
		t.Fatal("favicon discovery or download reached loopback server")
	}
}

func TestOversizedFaviconIsRemoved(t *testing.T) {
	icon, err := url.Parse("https://site.example.com/icon.png")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"image/png"}},
			Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxFaviconBytes+1))), Request: r,
		}, nil
	})}
	dir := t.TempDir()
	if _, downloadErr := downloadFaviconWithClient(icon, icon, dir, 1, client); downloadErr == nil {
		t.Fatal("oversized favicon was accepted")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("oversized favicon left %d files", len(files))
	}
}
