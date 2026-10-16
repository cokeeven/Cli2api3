package updatenet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadRejectsInvalidNetworkPolicy(t *testing.T) {
	for _, body := range []string{
		`{"api_mirrors":["http://mirror.example"]}`,
		`{"download_mirrors":["https://user:secret@mirror.example"]}`,
		`{"api_mirrors":["https://mirror.example?token=secret"]}`,
		`{"image_mirrors":["--privileged"]}`,
		`{"image_mirrors":["mirror.example/app:latest"]}`,
		`{"image_mirrors":["mirror.example/app@sha256:abc"]}`,
		`{"request_timeout_seconds":0}`,
		`{"pull_timeout_seconds":9999}`,
		`{"proxy_url":"file:///secret"}`,
		`{"typo":true}`,
		`{} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "network.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestLoadDefaultsAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network.json")
	c, err := Load(path)
	if err != nil || c.RequestTimeout != 8 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`{"api_mirrors":["https://mirror.example/api"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil || len(c.APIMirrors) != 1 || c.DownloadTimeout != 120 {
		t.Fatalf("reload: %+v %v", c, err)
	}
}

func TestProxyRouteActuallyUsesConfiguredProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "unreachable.invalid" {
			t.Errorf("proxy target = %s", r.URL.Host)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	client := Client(nil, proxy.URL, time.Second, func(_ *url.URL) bool { return true })
	defer client.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://unreachable.invalid/file", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.StatusCode)
	}
}
