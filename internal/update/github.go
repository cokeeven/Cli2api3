package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/updatenet"
)

const defaultGitHubAPI = "https://api.github.com"

type GitHubReleaseSource struct {
	client      *http.Client
	baseURL     string
	repo        string
	token       string
	networkFile string
	mu          sync.Mutex
	network     updatenet.Status
}

func NewGitHubReleaseSource(repo, token string, networkFile ...string) *GitHubReleaseSource {
	client := &http.Client{}
	source := &GitHubReleaseSource{client: client, baseURL: defaultGitHubAPI, repo: repo, token: strings.TrimSpace(token)}
	if len(networkFile) > 0 {
		source.networkFile = networkFile[0]
	}
	return source
}

func (s *GitHubReleaseSource) NetworkStatus() updatenet.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.network
}

func (s *GitHubReleaseSource) setNetwork(status updatenet.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.network = status
}

func (s *GitHubReleaseSource) ListReleases(ctx context.Context) ([]Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if strings.TrimSpace(s.repo) == "" {
		return nil, fmt.Errorf("update repository required")
	}
	config, err := updatenet.Load(s.networkFile)
	if err != nil {
		s.setNetwork(updatenet.Status{Error: err.Error()})
		return nil, err
	}
	status := config.Status()
	s.setNetwork(status)
	failures := make([]string, 0)
	for index, base := range append([]string{s.baseURL}, config.APIMirrors...) {
		for _, proxy := range updatenet.Routes(config.ProxyURL) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			client := updatenet.Client(s.client, proxy, time.Duration(config.RequestTimeout)*time.Second, func(u *url.URL) bool { return isTrustedGitHubAPI(u) || updatenet.SameOrigin(base, u) })
			releases, err := s.listAll(ctx, base, client)
			client.CloseIdleConnections()
			label := updatenet.Label(base)
			if proxy != "" {
				label += " (proxy)"
			}
			if err == nil {
				status.Source, status.Fallback = label, index > 0 || proxy != ""
				s.setNetwork(status)
				return releases, nil
			}
			failures = append(failures, label+": "+err.Error())
		}
	}
	status.Error = "all release sources failed: " + strings.Join(failures, "; ")
	s.setNetwork(status)
	return nil, fmt.Errorf("%s", status.Error)
}

func (s *GitHubReleaseSource) listAll(ctx context.Context, base string, client *http.Client) ([]Release, error) {
	releases := make([]Release, 0, 100)
	for page := 1; page <= 100; page++ {
		batch, err := s.listReleasePage(ctx, base, client, page)
		if err != nil {
			return nil, err
		}
		releases = append(releases, batch...)
		if len(batch) < 100 {
			return releases, nil
		}
	}
	return nil, fmt.Errorf("release pagination limit exceeded")
}

func (s *GitHubReleaseSource) listReleasePage(ctx context.Context, base string, client *http.Client, page int) ([]Release, error) {
	requestURL := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", strings.TrimRight(base, "/"), s.repo, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cli2api-updater")
	if s.token != "" && isTrustedGitHubAPI(req.URL) {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases returned %d", resp.StatusCode)
	}
	var releases []Release
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	if err := decoder.Decode(&releases); err != nil {
		return nil, fmt.Errorf("invalid releases JSON")
	}
	if releases == nil {
		return nil, fmt.Errorf("releases response must be an array")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after releases JSON")
	}
	return releases, nil
}

func isTrustedGitHubAPI(value *url.URL) bool {
	return value != nil && value.Scheme == "https" && value.User == nil && strings.EqualFold(value.Host, "api.github.com")
}
