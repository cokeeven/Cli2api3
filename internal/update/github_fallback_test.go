package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fallbackTransport func(*http.Request) (*http.Response, error)

func (f fallbackTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func networkConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReleaseFallbackDiscardsPartialPrimaryAndDoesNotLeakToken(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("GitHub token leaked to mirror")
		}
		if r.URL.Path != "/api/repos/cokeeven/ali2api3/releases" {
			t.Errorf("mirror path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"tag_name":"v0.1.9"}]`)
	}))
	defer mirror.Close()
	source := NewGitHubReleaseSource("cokeeven/ali2api3", "private-token", networkConfigFile(t, fmt.Sprintf(`{"api_mirrors":[%q]}`, mirror.URL+"/api")))
	source.client.Transport = fallbackTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("origin auth missing")
			}
			body, status := `[`, http.StatusOK
			if r.URL.Query().Get("page") == "1" {
				body += strings.Repeat(`{"tag_name":"v0.1.1"},`, 99) + `{"tag_name":"v0.1.1"}]`
			} else {
				body, status = "unavailable", http.StatusServiceUnavailable
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	releases, err := source.ListReleases(context.Background())
	if err != nil || len(releases) != 1 || releases[0].TagName != "v0.1.9" {
		t.Fatalf("fallback = %+v %v", releases, err)
	}
	status := source.NetworkStatus()
	if !status.Fallback || status.APIMirrors != 1 || status.Source == "api.github.com" {
		t.Fatalf("status = %+v", status)
	}
}

func TestReleaseInvalidPrimaryJSONFallsBackAndCancellationStops(t *testing.T) {
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }))
	defer mirror.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `<html>gateway</html>`) }))
	defer primary.Close()
	source := NewGitHubReleaseSource("cokeeven/ali2api3", "", networkConfigFile(t, fmt.Sprintf(`{"api_mirrors":[%q]}`, mirror.URL)))
	source.baseURL = primary.URL
	if _, err := source.ListReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.ListReleases(ctx); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestReleaseRedirectDoesNotLeakTokenToSubdomain(t *testing.T) {
	source := NewGitHubReleaseSource("cokeeven/ali2api3", "private-token")
	source.client.Transport = fallbackTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://downloads.api.github.com/leak"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		t.Fatal("untrusted redirect was followed")
		return nil, fmt.Errorf("untrusted redirect")
	})
	if _, err := source.ListReleases(context.Background()); err == nil {
		t.Fatal("untrusted redirect accepted")
	}
}

func TestReleaseTimeoutFallsBackWithinBound(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer primary.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }))
	defer mirror.Close()
	source := NewGitHubReleaseSource("cokeeven/ali2api3", "", networkConfigFile(t, fmt.Sprintf(`{"request_timeout_seconds":1,"api_mirrors":[%q]}`, mirror.URL)))
	source.baseURL = primary.URL
	start := time.Now()
	if _, err := source.ListReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout fallback took %v", elapsed)
	}
}
