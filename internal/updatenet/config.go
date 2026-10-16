// Package updatenet contains the operator-controlled network policy shared by
// the application and its host updater. It never changes Docker daemon policy.
package updatenet

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	APIMirrors      []string `json:"api_mirrors"`
	DownloadMirrors []string `json:"download_mirrors"`
	ImageMirrors    []string `json:"image_mirrors"`
	ProxyURL        string   `json:"proxy_url"`
	RequestTimeout  int      `json:"request_timeout_seconds"`
	DownloadTimeout int      `json:"download_timeout_seconds"`
	PullTimeout     int      `json:"pull_timeout_seconds"`
}

// Status contains no credentials or local paths and is safe for the console.
type Status struct {
	Source           string `json:"source,omitempty"`
	ImageSource      string `json:"image_source,omitempty"`
	DownloadSource   string `json:"download_source,omitempty"`
	ImageFallback    bool   `json:"image_fallback"`
	DownloadFallback bool   `json:"download_fallback"`
	Fallback         bool   `json:"fallback"`
	APIMirrors       int    `json:"api_mirrors"`
	DownloadMirrors  int    `json:"download_mirrors"`
	ImageMirrors     int    `json:"image_mirrors"`
	Proxy            bool   `json:"proxy"`
	Error            string `json:"error,omitempty"`
}

func (c Config) Status() Status {
	return Status{APIMirrors: len(c.APIMirrors), DownloadMirrors: len(c.DownloadMirrors), ImageMirrors: len(c.ImageMirrors), Proxy: c.ProxyURL != ""}
}

func Load(path string) (Config, error) {
	c := Config{RequestTimeout: 8, DownloadTimeout: 120, PullTimeout: 180}
	if path == "" {
		return c, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("cannot read update network configuration")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid update network configuration JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("update network configuration must contain one JSON object")
	}
	if c.RequestTimeout < 1 || c.RequestTimeout > 30 || c.DownloadTimeout < 1 || c.DownloadTimeout > 300 || c.PullTimeout < 1 || c.PullTimeout > 300 {
		return c, fmt.Errorf("update network timeouts out of range (request 1-30, download/pull 1-300 seconds)")
	}
	for _, list := range [][]string{c.APIMirrors, c.DownloadMirrors} {
		if len(list) > 3 {
			return c, fmt.Errorf("at most three mirrors per update channel")
		}
		for _, raw := range list {
			if err := ValidateURL(raw); err != nil {
				return c, err
			}
		}
	}
	if len(c.ImageMirrors) > 3 {
		return c, fmt.Errorf("at most three image mirrors")
	}
	for _, repo := range c.ImageMirrors {
		if !ValidImageRepository(repo) {
			return c, fmt.Errorf("invalid update image mirror repository")
		}
	}
	if c.ProxyURL != "" {
		u, err := url.Parse(c.ProxyURL)
		if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return c, fmt.Errorf("invalid update proxy URL")
		}
	}
	return c, nil
}

func ValidImageRepository(repo string) bool {
	// The operator supplies a repository, never Docker flags, a URL, tag or digest.
	if repo == "" || repo != strings.TrimSpace(repo) || !strings.Contains(repo, "/") || strings.ContainsAny(repo, "@?#\\ \t\r\n") || strings.Contains(repo, "://") || strings.HasPrefix(repo, "-") || strings.HasSuffix(repo, "/") {
		return false
	}
	parts := strings.SplitN(repo, "/", 2)
	if strings.Contains(parts[1], ":") {
		return false
	}
	for _, ch := range repo {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || strings.ContainsRune("./_-:", ch)) {
			return false
		}
	}
	return !strings.Contains(repo, "..") && !strings.Contains(repo, "//")
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid update mirror URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return fmt.Errorf("update mirrors require HTTPS (HTTP allowed only on loopback)")
	}
	return nil
}

func SameOrigin(raw string, u *url.URL) bool {
	base, err := url.Parse(raw)
	return err == nil && u != nil && base.Scheme == u.Scheme && strings.EqualFold(base.Host, u.Host) && u.User == nil
}

func Label(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "unknown"
	}
	return u.Host
}

// Client first tries direct/environment networking. Explicit proxy is a second
// route, so an unavailable proxy cannot break a working direct connection.
func Client(base *http.Client, proxy string, timeout time.Duration, allowed func(*url.URL) bool) *http.Client {
	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	client.Timeout = timeout
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if old, ok := client.Transport.(*http.Transport); ok {
		transport = old.Clone()
	}
	if proxy != "" {
		parsed, _ := url.Parse(proxy)
		transport.Proxy = http.ProxyURL(parsed)
	}
	if _, ok := client.Transport.(*http.Transport); client.Transport == nil || ok || proxy != "" {
		client.Transport = transport
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many update redirects")
		}
		if !allowed(req.URL) {
			return fmt.Errorf("untrusted update redirect")
		}
		if len(via) > 0 && !SameOrigin(via[0].URL.String(), req.URL) {
			req.Header.Del("Authorization")
		}
		return nil
	}
	return client
}

func Routes(proxy string) []string {
	if proxy == "" {
		return []string{""}
	}
	return []string{"", proxy}
}
