package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	proxyutil "github.com/caigee-cmd/cli2api/internal/proxy"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

// Store is the persistence surface the adapter needs. It matches
// the SQLite store without importing the concrete type.
type Store interface {
	Get(ctx context.Context, id string) (accounts.Account, error)
	LoadCredentialPayload(ctx context.Context, accountID string) (string, []byte, error)
	SaveCredentialPayload(ctx context.Context, accountID, format string, payload []byte) error
	Observe(ctx context.Context, id, remoteUID, status, lastError, lastKind string) error
}

// SecretReader is optional. Missing it means no global proxy, not an error.
type SecretReader interface {
	GetSecret(context.Context, string) (string, bool, error)
}

// ModelSettingReader is optional. Missing it means console-saved reasoning
// defaults are skipped, matching the previous anonymous type assertion.
type ModelSettingReader interface {
	GetProviderModelSetting(context.Context, string, string) (accounts.ProviderModelSetting, error)
}

type Client struct {
	store Store
	http  *http.Client

	transports proxyutil.TransportCache

	mu                   sync.Mutex
	loginStates          map[string]string
	catalog              map[string]providers.ModelInfo
	credentialLocks      map[string]*sync.Mutex
	credentialRetryAfter map[string]time.Time
}

const catalogTimeout = 15 * time.Second

var (
	dailyCheckinRetryDelays           = []time.Duration{time.Second, 3 * time.Second}
	dailyCheckinProcessingRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
)

func NewClient(store Store) *Client {
	return &Client{
		store: store,
		http: &http.Client{
			Timeout: 120 * time.Second,
			// Catalog and plugin APIs 302 to OIDC HTML when the path is a
			// console page. Following that rewrite turns a 302 into a login
			// document and hides the real status.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		loginStates:          map[string]string{},
		credentialLocks:      map[string]*sync.Mutex{},
		credentialRetryAfter: map[string]time.Time{},
	}
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (c *Client) globalProxy(ctx context.Context) (string, error) {
	store, ok := c.store.(SecretReader)
	if !ok {
		return "", nil
	}

	value, found, err := store.GetSecret(ctx, "proxy_url")
	if err != nil {
		return "", fmt.Errorf("load global proxy setting: %w", err)
	}
	if !found {
		return "", nil
	}
	return strings.TrimSpace(value), nil
}

func (c *Client) effectiveProxy(ctx context.Context, accountID string) (string, error) {
	account, err := c.store.Get(ctx, accountID)
	if err != nil {
		return "", err
	}

	if value := strings.TrimSpace(account.ProxyURL); value != "" {
		return value, nil
	}

	return c.globalProxy(ctx)
}

func (c *Client) httpClient(ctx context.Context, accountID string) (*http.Client, error) {
	rawProxy, err := c.effectiveProxy(ctx, accountID)
	if err != nil {
		return nil, err
	}

	client := *c.http

	transport, err := c.transports.Get(rawProxy)
	if err != nil {
		return nil, err
	}
	if transport != nil {
		client.Transport = transport
	}

	return &client, nil
}

func (c *Client) do(ctx context.Context, accountID, method, rawURL string, body []byte, setHeaders func(http.Header)) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, err
	}
	if setHeaders != nil {
		setHeaders(req.Header)
	}
	client, err := c.httpClient(ctx, accountID)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return payload, resp.StatusCode, nil
}

// StartLogin requests a server-issued state and returns the browser URL.
func (c *Client) StartLogin(ctx context.Context, accountID string) (providers.LoginSession, error) {
	base := ChatBaseCN
	if account, err := c.store.Get(ctx, accountID); err == nil && account.ProviderRegion == "global" {
		base = ChatBaseGlobal
	}
	body, status, err := c.do(ctx, accountID, http.MethodPost, base+pathAuthState+"?platform=CLI", []byte("{}"),
		func(h http.Header) { setCommonHeaders(h, base == ChatBaseGlobal) })
	if err != nil {
		return providers.LoginSession{}, err
	}
	if status >= 300 {
		return providers.LoginSession{}, fmt.Errorf("auth state status=%d: %s", status, string(body))
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Code != 0 {
		return providers.LoginSession{}, fmt.Errorf("auth state failed: code=%d msg=%s", env.Code, env.Msg)
	}
	var data struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.State == "" {
		return providers.LoginSession{}, fmt.Errorf("auth state response missing state")
	}
	c.mu.Lock()
	c.loginStates[accountID] = data.State
	c.mu.Unlock()
	return providers.LoginSession{AuthURL: data.AuthURL, State: data.State}, nil
}

// PollLogin exchanges the state for tokens and account identity, then stores
// the canonical credential payload.
func (c *Client) PollLogin(ctx context.Context, accountID string) (bool, string, error) {
	c.mu.Lock()
	state := c.loginStates[accountID]
	c.mu.Unlock()
	if state == "" {
		return false, "", fmt.Errorf("login not started for account %s", accountID)
	}
	base := ChatBaseCN
	if account, err := c.store.Get(ctx, accountID); err == nil && account.ProviderRegion == "global" {
		base = ChatBaseGlobal
	}
	tokenBody, status, err := c.do(ctx, accountID, http.MethodGet, base+pathAuthToken+"?state="+url.QueryEscape(state), nil,
		func(h http.Header) { setCommonHeaders(h, base == ChatBaseGlobal) })
	if err != nil {
		return false, "", err
	}
	var tokenEnv envelope
	_ = json.Unmarshal(tokenBody, &tokenEnv)
	if status >= 500 {
		return false, "", fmt.Errorf("token endpoint status=%d", status)
	}
	if status >= 300 || tokenEnv.Code != 0 {
		return false, "waiting for authorization", nil
	}
	var token struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokenEnv.Data, &token); err != nil || token.AccessToken == "" {
		return false, "waiting for authorization", nil
	}
	accountBody, _, err := c.do(ctx, accountID, http.MethodGet, base+pathAuthAccount+"?state="+url.QueryEscape(state), nil,
		func(h http.Header) {
			setCommonHeaders(h, base == ChatBaseGlobal)
			h.Set("Authorization", "Bearer "+token.AccessToken)
		})
	if err != nil {
		return false, "", err
	}
	var accountEnv envelope
	var identity struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	if json.Unmarshal(accountBody, &accountEnv) == nil && accountEnv.Code == 0 {
		_ = json.Unmarshal(accountEnv.Data, &identity)
	}
	domain := token.Domain
	if domain == "" || (base == ChatBaseGlobal && !strings.Contains(strings.ToLower(domain), DomainGlobal) && !strings.Contains(strings.ToLower(domain), "workbuddy")) {
		if base == ChatBaseGlobal {
			domain = DomainGlobal
		} else if domain == "" {
			domain = DomainCN
		}
	}
	credential := Credential{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix(),
		Domain:       domain,
		UID:          identity.UID,
		EnterpriseID: identity.EnterpriseID,
		Nickname:     identity.Nickname,
	}
	payload, err := credential.Encode()
	if err != nil {
		return false, "", err
	}
	if err := c.store.SaveCredentialPayload(ctx, accountID, CredentialFormat, payload); err != nil {
		return false, "", err
	}
	_ = c.store.Observe(ctx, accountID, identity.UID, "ready", "", "")
	c.mu.Lock()
	delete(c.loginStates, accountID)
	c.mu.Unlock()
	return true, "login complete", nil
}

// credential loads and, when nearly expired, refreshes the stored token.
// Refreshes are serialized per account and saved with a compare-and-swap so a
// late refresh cannot replace a newer login.
func (c *Client) credential(ctx context.Context, accountID string) (Credential, error) {
	format, payload, err := c.store.LoadCredentialPayload(ctx, accountID)
	if err != nil {
		return Credential{}, err
	}
	credential, err := DecodeCredential(payload)
	if err != nil {
		return Credential{}, err
	}
	if !credential.needsRefresh(time.Now()) {
		return credential, nil
	}
	lock := c.credentialLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	format, payload, credential, err = c.loadCredential(ctx, accountID)
	if err != nil {
		return Credential{}, err
	}
	now := time.Now()
	if !credential.needsRefresh(now) {
		return credential, nil
	}
	if c.credentialRefreshCoolingDown(accountID, now) {
		return c.useWorkBuddyCredentialFallback(credential, now, fmt.Errorf("credential refresh is cooling down"))
	}
	refreshed, err := c.refreshRemote(ctx, accountID, credential)
	if err != nil {
		c.recordCredentialRefreshFailure(accountID, err)
		return c.useWorkBuddyCredentialFallback(credential, now, err)
	}
	encoded, err := refreshed.Encode()
	if err != nil {
		return Credential{}, err
	}
	saved, err := c.saveCredentialIfCurrent(ctx, accountID, format, payload, encoded)
	if err != nil {
		return Credential{}, err
	}
	if !saved {
		_, _, latest, loadErr := c.loadCredential(ctx, accountID)
		if loadErr != nil {
			return Credential{}, loadErr
		}
		return latest, nil
	}
	c.clearCredentialRefreshFailure(accountID)
	return refreshed, nil
}

func (c *Client) loadCredential(ctx context.Context, accountID string) (string, []byte, Credential, error) {
	format, payload, err := c.store.LoadCredentialPayload(ctx, accountID)
	if err != nil {
		return "", nil, Credential{}, err
	}
	credential, err := DecodeCredential(payload)
	return format, payload, credential, err
}

func (c *Client) credentialLock(accountID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.credentialLocks == nil {
		c.credentialLocks = map[string]*sync.Mutex{}
	}
	lock := c.credentialLocks[accountID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.credentialLocks[accountID] = lock
	}
	return lock
}

func (c *Client) credentialRefreshCoolingDown(accountID string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until := c.credentialRetryAfter[accountID]
	if !now.Before(until) {
		delete(c.credentialRetryAfter, accountID)
		return false
	}
	return true
}

func (c *Client) recordCredentialRefreshFailure(accountID string, err error) {
	delay := 30 * time.Second
	var classified *providers.Error
	if errors.As(err, &classified) && classified.RetryAfter > delay {
		delay = classified.RetryAfter
	}
	c.mu.Lock()
	if c.credentialRetryAfter == nil {
		c.credentialRetryAfter = map[string]time.Time{}
	}
	c.credentialRetryAfter[accountID] = time.Now().Add(delay)
	c.mu.Unlock()
}

func (c *Client) clearCredentialRefreshFailure(accountID string) {
	c.mu.Lock()
	delete(c.credentialRetryAfter, accountID)
	c.mu.Unlock()
}

func (c *Client) useWorkBuddyCredentialFallback(credential Credential, now time.Time, refreshErr error) (Credential, error) {
	if credential.ExpiresAt > now.Unix() {
		return credential, nil
	}
	return Credential{}, fmt.Errorf("workbuddy access token expired and refresh failed: %w", refreshErr)
}

func credentialProbeErrorKind(err error) string {
	var classified *providers.Error
	if errors.As(err, &classified) && classified.Kind != "" {
		return classified.Kind
	}
	return accounts.KindUnavailable
}

func (c *Client) overlayRegion(ctx context.Context, accountID string, credential Credential) Credential {
	account, err := c.store.Get(ctx, accountID)
	if err != nil {
		return credential
	}
	if account.ProviderRegion == "global" && !credential.IsGlobal() {
		credential.Domain = DomainGlobal
	}
	if account.ProviderRegion == "cn" && credential.IsGlobal() {
		credential.Domain = DomainCN
	}
	return credential
}

func (c *Client) resolvedCredential(ctx context.Context, accountID string) (Credential, error) {
	credential, err := c.credential(ctx, accountID)
	if err != nil {
		return Credential{}, err
	}
	return c.overlayRegion(ctx, accountID, credential), nil
}

// Refresh exchanges the refresh token. Session-dead errors disable the
// account by surfacing the auth taxonomy to the manager.
func (c *Client) Refresh(ctx context.Context, accountID string, credential Credential) (Credential, error) {
	lock := c.credentialLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	format, payload, current, err := c.loadCredential(ctx, accountID)
	if err != nil {
		return Credential{}, err
	}
	credential = current
	refreshed, err := c.refreshRemote(ctx, accountID, credential)
	if err != nil {
		c.recordCredentialRefreshFailure(accountID, err)
		return Credential{}, err
	}
	encoded, err := refreshed.Encode()
	if err != nil {
		return Credential{}, err
	}
	saved, err := c.saveCredentialIfCurrent(ctx, accountID, format, payload, encoded)
	if err != nil {
		return Credential{}, err
	}
	if !saved {
		_, _, latest, loadErr := c.loadCredential(ctx, accountID)
		if loadErr != nil {
			return Credential{}, loadErr
		}
		return latest, nil
	}
	c.clearCredentialRefreshFailure(accountID)
	return refreshed, nil
}

func (c *Client) saveCredentialIfCurrent(ctx context.Context, accountID, expectedFormat string, expectedPayload, payload []byte) (bool, error) {
	store, ok := c.store.(providers.CredentialPayloadCASStore)
	if !ok {
		return false, fmt.Errorf("workbuddy credential store does not support compare-and-swap persistence")
	}
	return store.SaveCredentialPayloadIfCurrent(ctx, accountID, expectedFormat, expectedPayload, CredentialFormat, payload)
}

func (c *Client) refreshRemote(ctx context.Context, accountID string, credential Credential) (Credential, error) {
	credential = c.overlayRegion(ctx, accountID, credential)
	body, status, err := c.do(ctx, accountID, http.MethodPost, credential.ChatBase()+pathTokenRefresh, []byte("{}"),
		func(h http.Header) { SetRefreshHeaders(h, credential) })
	if err != nil {
		return credential, err
	}
	classified := Classify(status, string(body))
	if classified.Kind == accounts.KindAuth {
		if credential.ExpiresAt <= time.Now().Unix() {
			_ = c.store.Observe(ctx, accountID, credential.UID, "login_required", "session dead; re-login required", accounts.KindAuth)
		}
		return credential, classifiedError(status, body)
	}
	if status >= 300 || classified.Kind != "" {
		return credential, classifiedError(status, body)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || env.Code != 0 {
		return credential, fmt.Errorf("refresh envelope code=%d msg=%s", env.Code, env.Msg)
	}
	var data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.AccessToken == "" {
		return credential, fmt.Errorf("refresh response missing accessToken")
	}
	if data.RefreshToken != "" {
		credential.RefreshToken = data.RefreshToken
	}
	if data.Domain != "" {
		credential.Domain = data.Domain
	}
	if data.ExpiresIn > 0 {
		credential.ExpiresAt = time.Now().Add(time.Duration(data.ExpiresIn) * time.Second).Unix()
	}
	credential.RefreshAfter = workBuddyRefreshAfter(credential.ExpiresAt, time.Now())
	credential.AccessToken = data.AccessToken
	credential = c.overlayRegion(ctx, accountID, credential)
	return credential, nil
}

func workBuddyRefreshAfter(expiresAt int64, now time.Time) int64 {
	if expiresAt <= 0 {
		return now.Add(5 * time.Minute).Unix()
	}
	remaining := time.Until(time.Unix(expiresAt, 0))
	if remaining <= 0 {
		return now.Unix()
	}
	lead := 24 * time.Hour
	if half := remaining / 2; half < lead {
		lead = half
	}
	return time.Unix(expiresAt, 0).Add(-lead).Unix()
}

// Models fetches the IDE-parity product config catalog (/v3/config). Failure
// is an explicit error; there is no static fallback list. This intentionally
// mirrors the WorkBuddy desktop dropdown source rather than
// /v2/enterprises/personal/models, which can omit IDE-visible ids.
func (c *Client) Models(ctx context.Context, accountID string) ([]providers.ModelInfo, error) {
	credential, err := c.resolvedCredential(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	body, status, err := c.do(ctx, accountID, http.MethodGet, credential.ChatBase()+credential.productConfigPath(), nil,
		func(h http.Header) { SetCatalogHeaders(h, credential) })
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("models status=%d: %s", status, catalogErrorBody(body))
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Models []catalogModelEntry `json:"models"`
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("models parse: %s", catalogErrorBody(body))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("models envelope code=%d msg=%s", env.Code, env.Msg)
	}
	if env.Data.Models == nil {
		return nil, fmt.Errorf("workbuddy product config returned no models")
	}
	// IDE dropdown intersects product-config models with the CLI agent
	// allowlist from the same /v3/config payload. If agents are absent,
	// keep every enabled model. Do not invent public aliases.
	cliModels := map[string]struct{}{}
	for _, agent := range env.Data.Agents {
		if !isCLIAgent(agent.Name) {
			continue
		}
		for _, id := range agent.Models {
			cliModels[id] = struct{}{}
		}
	}
	filterCLI := len(cliModels) > 0
	var out []providers.ModelInfo
	for _, model := range env.Data.Models {
		if model.Disabled {
			continue
		}
		if filterCLI {
			if _, ok := cliModels[model.ID]; !ok {
				continue
			}
		}
		out = append(out, catalogModel(model))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("workbuddy product config returned no models")
	}
	c.rememberCatalog(out)
	return out, nil
}

func (c *Client) chatRequest(ctx context.Context, accountID string, credential Credential, req translate.ChatRequest) (*http.Request, providers.ResolvedChat, error) {
	if err := translate.ValidateChatToolHistory(req); err != nil {
		return nil, providers.ResolvedChat{}, &providers.Error{Kind: "invalid_request", Status: 400, Code: "invalid_tool_history", Message: err.Error()}
	}
	caps := c.capsFor(req.Model)
	storedLevel := ""
	if setter, ok := c.store.(ModelSettingReader); ok {
		// CanonicalModelID must match control.ModelContextKey
		// so console-saved reasoning levels are found at chat time.
		if stored, err := setter.GetProviderModelSetting(ctx, "workbuddy", accounts.CanonicalModelID(req.Model)); err == nil {
			storedLevel = stored.ReasoningEffort
		}
	}
	// Warm the live catalog before resolving the upstream model id and
	// reasoning caps. Catalog entries are authoritative; we do not invent
	// aliases when a requested id is missing.
	if (!c.hasCatalogEntry(req.Model) || len(caps.ReasoningOptions) == 0) && accountID != "" {
		_, _ = c.Models(ctx, accountID)
		caps = c.capsFor(req.Model)
	}
	if err := validateCallerReasoning(req, caps); err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	body := map[string]any{
		"model":       c.upstreamModelID(req.Model),
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"tools":       req.Tools,
		"tool_choice": req.ToolChoice,
	}
	outputLimit, outputSource, err := providers.ResolveOutputLimit(req, caps, false)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	if outputLimit != nil {
		body["max_tokens"] = *outputLimit
	}
	resolved := providers.ResolvedChat{
		ReasoningLevel: applyChatReasoning(body, req, storedLevel, caps),
		OutputLimit:    outputLimit, OutputLimitSource: outputSource,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		credential.ChatBase()+pathChat, bytes.NewReader(PrepareBody(payload)))
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	SetChatHeaders(httpReq.Header, credential)
	return httpReq, resolved, nil
}

func (c *Client) ChatNonStream(ctx context.Context, accountID string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	credential, err := c.resolvedCredential(ctx, accountID)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	httpReq, resolved, err := c.chatRequest(ctx, accountID, credential, req)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	client, err := c.httpClient(ctx, accountID)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	resp, err := providers.DoChatRequest(client, httpReq)
	if err != nil {
		return resolved.OutcomeMetadata(), err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if len(body) > 16<<20 {
		body = body[:16<<20]
		readErr = fmt.Errorf("workbuddy response exceeds 16 MiB")
	}
	if resp.StatusCode >= 300 {
		return resolved.OutcomeMetadata(), providers.WithRetryAfterHeader(classifiedError(resp.StatusCode, body), resp.Header.Get("Retry-After"))
	}
	aggregate, aggregateErr := Aggregate(bytes.NewReader(body))
	outcome, err := outcomeFromAggregate(aggregate)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	outcome.ReasoningLevel = resolved.ReasoningLevel
	outcome.OutputLimit = resolved.OutputLimit
	outcome.OutputLimitSource = resolved.OutputLimitSource
	return outcome, providers.WithRetryAfterHeader(errors.Join(readErr, aggregateErr), resp.Header.Get("Retry-After"))
}

func (c *Client) ChatStream(ctx context.Context, accountID string, req translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	credential, err := c.resolvedCredential(ctx, accountID)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	httpReq, resolved, err := c.chatRequest(ctx, accountID, credential, req)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	client, err := c.httpClient(ctx, accountID)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	resp, err := providers.DoChatRequest(client, httpReq)
	if err != nil {
		return nil, resolved, err
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, resolved, providers.WithRetryAfterHeader(classifiedError(resp.StatusCode, body), resp.Header.Get("Retry-After"))
	}
	streamResp := rewriteChatStream(resp)
	streamResp.Body = providers.CarryChatObservation(streamResp.Body, resp.Body, resp.Header.Get("Retry-After"))
	return streamResp, resolved, nil
}

func outcomeFromAggregate(aggregate map[string]any) (providers.ChatOutcome, error) {
	raw, err := json.Marshal(aggregate)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	var parsed struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens             *int     `json:"prompt_tokens"`
			CompletionTokens         *int     `json:"completion_tokens"`
			CacheReadTokens          *int     `json:"cache_read_tokens"`
			CacheWriteTokens         *int     `json:"cache_write_tokens"`
			PromptCacheHitTokens     *int     `json:"prompt_cache_hit_tokens"`
			CacheReadInputTokens     *int     `json:"cache_read_input_tokens"`
			CacheCreationInputTokens *int     `json:"cache_creation_input_tokens"`
			Source                   string   `json:"source"`
			Credit                   *float64 `json:"credit"`
			PromptDetails            struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return providers.ChatOutcome{}, err
	}
	out := providers.ChatOutcome{UsageSource: "unknown", FinishReason: "stop", FinishReasonSource: "adapter_default"}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
		out.Reasoning = parsed.Choices[0].Message.ReasoningContent
		out.ToolCalls = parsed.Choices[0].Message.ToolCalls
		if parsed.Choices[0].FinishReason != "" {
			out.FinishReason = parsed.Choices[0].FinishReason
			out.FinishReasonSource = "upstream"
		}
	}
	out.Model = parsed.Model
	if parsed.Usage.PromptTokens != nil {
		out.PromptTokens = *parsed.Usage.PromptTokens
	}
	if parsed.Usage.CompletionTokens != nil {
		out.CompletionTokens = *parsed.Usage.CompletionTokens
	}
	out.ReportedPromptTokens, out.ReportedCompletionTokens = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	out.UsageExtensions = providers.NumericUsageExtensions(aggregate["usage"])
	if parsed.Usage.PromptTokens != nil && parsed.Usage.CompletionTokens != nil {
		out.UsageSource = parsed.Usage.Source
		if out.UsageSource == "" {
			out.UsageSource = "upstream"
		}
	}
	out.CacheReadTokens = providers.FirstIntPointer(parsed.Usage.CacheReadTokens, parsed.Usage.PromptDetails.CachedTokens, parsed.Usage.PromptCacheHitTokens, parsed.Usage.CacheReadInputTokens)
	out.CachedTokens = out.CacheReadTokens
	out.CacheWriteTokens = providers.FirstIntPointer(parsed.Usage.CacheWriteTokens, parsed.Usage.CacheCreationInputTokens)
	out.Credits = parsed.Usage.Credit
	return out, nil
}

func classifiedError(status int, body []byte) error {
	classified := Classify(status, string(body))
	out := &providers.Error{Kind: classified.Kind, Status: classified.Status, Message: classified.Message, Scope: classified.Scope, RetrySafe: classified.RetrySafe, Code: classified.Code}
	if classified.Kind == accounts.KindRateLimit {
		if resetAt, ok := parseQuotaResetAt(string(body), time.Now()); ok {
			out.UpstreamResetAt = &resetAt
			out.RetryAfter = resetAt.Sub(time.Now())
		}
	}
	return out
}

var quotaResetPattern = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})`)

// parseQuotaReset extracts the absolute reset time WorkBuddy embeds in its
// usage-limit message, e.g.
//
//	{"code":6004,"msg":"您的使用量已超出频率限制，将在 2026-09-01 13:56:47 UTC+8 重置"}
//
// The timestamp is wall-clock local time (UTC+8 for the CN deployment), so it
// is interpreted in that fixed zone and converted to a remaining duration.
// Returns 0 when no reset time is present, leaving the caller's fallback.
func parseQuotaReset(body string, now time.Time) time.Duration {
	resetAt, ok := parseQuotaResetAt(body, now)
	if !ok {
		return 0
	}
	return resetAt.Sub(now)
}

func parseQuotaResetAt(body string, now time.Time) (time.Time, bool) {
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.Code != rateLimitCode {
		return time.Time{}, false
	}
	match := quotaResetPattern.FindStringSubmatch(env.Msg)
	if match == nil {
		return time.Time{}, false
	}
	atoi := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return -1
		}
		return n
	}
	year, month, day := atoi(match[1]), atoi(match[2]), atoi(match[3])
	hour, minute, second := atoi(match[4]), atoi(match[5]), atoi(match[6])
	// time.Date normalizes out-of-range values (month 13 becomes January of
	// the next year), so reject them explicitly instead of trusting the parse.
	if year < 0 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	if hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	resetAt := time.Date(year, time.Month(month), day, hour, minute, second, 0, quotaResetLocation)
	if resetAt.Day() != day || int(resetAt.Month()) != month || resetAt.Year() != year {
		return time.Time{}, false
	}
	if resetAt.Sub(now) <= 0 {
		return time.Time{}, false
	}
	return resetAt.UTC(), true
}

// ClassifiedError is the historical WorkBuddy alias for providers.Error.
type ClassifiedError = providers.Error

// Classify maps WorkBuddy HTTP/error bodies onto the internal taxonomy.
func Classify(status int, body string) providers.ClassifiedError {
	classified := classifyWorkBuddy(status, body)
	if rawCode := workBuddyBusinessCode(body); rawCode != "" {
		// Keep the provider's scalar code as diagnostic data. The taxonomy above
		// remains responsible for Kind, Scope, and retry safety; unknown codes do
		// not acquire semantics merely because their value was preserved.
		classified.Code = rawCode
	}
	return classified
}

func classifyWorkBuddy(status int, body string) providers.ClassifiedError {
	text := strings.ToLower(body)
	if accounts.IsPromptLimitText(body) {
		return providers.ClassifiedError{Kind: accounts.KindInvalidRequest, Status: 400, Message: strings.TrimSpace(body)}
	}
	var limit envelope
	_ = json.Unmarshal([]byte(body), &limit)
	switch {
	case limit.Code == rateLimitCode:
		return providers.ClassifiedError{Kind: accounts.KindRateLimit, Status: 429, Message: strings.TrimSpace(body), Code: strconv.Itoa(rateLimitCode), Scope: "model", RetrySafe: true}
	case strings.Contains(text, "insufficient credit") || strings.Contains(text, "insufficient balance") || strings.Contains(text, "积分不足") || strings.Contains(text, "余额不足"):
		return providers.ClassifiedError{Kind: accounts.KindQuota, Status: 402, Scope: "account", RetrySafe: true, Code: "insufficient_credits", Message: strings.TrimSpace(body)}
	case status == 402 || strings.Contains(text, "quota exceeded"):
		// A bare payment/quota status does not prove its scope or that retry
		// cannot duplicate an already accepted operation.
		return providers.ClassifiedError{Kind: accounts.KindQuota, Status: 402, Message: strings.TrimSpace(body)}
	case status == 401 || strings.Contains(text, sessionDeadText) ||
		strings.Contains(text, fmt.Sprintf("%d", sessionDeadCode)):
		return providers.ClassifiedError{Kind: accounts.KindAuth, Status: 401, Message: "session dead; re-login required"}
	case status == 429 || strings.Contains(text, "soft_rate"):
		return providers.ClassifiedError{Kind: accounts.KindRateLimit, Status: 429, Message: strings.TrimSpace(body)}
	case status == 404:
		return providers.ClassifiedError{Kind: accounts.KindUnavailable, Status: 404, Message: strings.TrimSpace(body)}
	case status == 400 || accounts.IsPromptLimitText(body) || accounts.IsInvalidRequestText(body) || isMissingSystemPrompt(body) || isBrokenToolSequence(body):
		// Request-level rejection (content screening, malformed fields,
		// missing leading system message): retrying on another account
		// cannot help and the account is healthy.
		return providers.ClassifiedError{Kind: accounts.KindInvalidRequest, Status: firstNonEmptyStatus(status, 400), Message: strings.TrimSpace(body)}
	case status >= 500:
		return providers.ClassifiedError{Kind: accounts.KindUnavailable, Status: status, Message: strings.TrimSpace(body)}
	}
	var env envelope
	if json.Unmarshal([]byte(body), &env) == nil && env.Code != 0 {
		if env.Code == sessionDeadCode || strings.Contains(strings.ToLower(env.Msg), sessionDeadText) {
			return providers.ClassifiedError{Kind: accounts.KindAuth, Status: 401, Message: "session dead; re-login required"}
		}
		if accounts.IsPromptLimitText(env.Msg) || accounts.IsInvalidRequestText(env.Msg) || isMissingSystemPrompt(env.Msg) || isBrokenToolSequence(env.Msg) || env.Code == missingSystemPromptCode || env.Code == toolCallSequenceCode {
			return providers.ClassifiedError{Kind: accounts.KindInvalidRequest, Status: firstNonEmptyStatus(status, 400), Message: env.Msg}
		}
		return providers.ClassifiedError{Kind: accounts.KindUnavailable, Status: 502, Message: env.Msg}
	}
	return providers.ClassifiedError{}
}

// workBuddyBusinessCode preserves a safe scalar business code even when this
// version does not know its meaning. The code remains diagnostic data; callers
// must not infer scope or retry safety from an unrecognized value.
func workBuddyBusinessCode(body string) string {
	var envelope struct {
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil || len(envelope.Code) == 0 {
		return ""
	}
	raw := bytes.TrimSpace(envelope.Code)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var code string
		if json.Unmarshal(raw, &code) != nil {
			return ""
		}
		return strings.TrimSpace(code)
	}
	if !json.Valid(raw) || bytes.ContainsAny(raw, "{}[]") {
		return ""
	}
	return string(raw)
}

func firstNonEmptyStatus(status, fallback int) int {
	if status >= 400 {
		return status
	}
	return fallback
}

func isMissingSystemPrompt(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, missingSystemPromptText) ||
		strings.Contains(lower, fmt.Sprintf("%d", missingSystemPromptCode))
}

func isBrokenToolSequence(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, toolCallSequenceText) ||
		strings.Contains(lower, "tool_call_sequence_broken") ||
		strings.Contains(lower, fmt.Sprintf("%d", toolCallSequenceCode))
}

func catalogErrorBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(text), "<html") || strings.Contains(strings.ToLower(text), "<!doctype") {
		lower := strings.ToLower(text)
		switch {
		case strings.Contains(lower, "502"), strings.Contains(lower, "bad gateway"):
			return "upstream html error page (bad gateway)"
		case strings.Contains(lower, "500"), strings.Contains(lower, "internal server error"):
			return "upstream html error page (internal server error)"
		case strings.Contains(lower, "openid-connect"), strings.Contains(lower, "auth/realms"):
			return "upstream html login redirect"
		default:
			return "upstream html error page"
		}
	}
	if len(text) > 240 {
		return text[:240]
	}
	return text
}

// Probe reports whether the stored credential is usable. There is no WASM hot
// state; Hot mirrors Ready so the accounts console can treat signed-in accounts
// as available without probing a child-process /health URL.
func (c *Client) Probe(ctx context.Context, accountID string) (providers.AccountHealth, error) {
	credential, err := c.resolvedCredential(ctx, accountID)
	if err != nil {
		return providers.AccountHealth{LastError: err.Error(), LastErrorKind: credentialProbeErrorKind(err)}, nil
	}
	if !credential.Ready() {
		msg := "workbuddy credential incomplete; re-login required"
		return providers.AccountHealth{UID: credential.UID, LastError: msg, LastErrorKind: accounts.KindAuth}, nil
	}
	return providers.AccountHealth{
		Ready: true,
		Hot:   true,
		UID:   credential.UID,
	}, nil
}

// Quota fetches remaining credits from the billing meter API.
// Failures return an error for the caller to ignore without flipping readiness.
func (c *Client) Quota(ctx context.Context, accountID string) (*providers.QuotaInfo, error) {
	credential, err := c.resolvedCredential(ctx, accountID)
	if err != nil {
		return nil, err
	}
	remain, used, total, packages, err := c.UserResource(ctx, accountID, credential)
	if err != nil {
		return nil, err
	}
	if total <= 0 && remain > 0 {
		total = remain
	}
	if used <= 0 && total > remain {
		used = total - remain
	}
	percentage := 0.0
	if total > 0 {
		percentage = (float64(used) / float64(total)) * 100
		if percentage < 0 {
			percentage = 0
		}
		if percentage > 100 {
			percentage = 100
		}
	}
	expiresAt, expiringRemain := soonestExpiry(packages)
	return &providers.QuotaInfo{
		Used:           float64(used),
		Total:          float64(total),
		Remaining:      float64(remain),
		Percentage:     percentage,
		Unit:           "credits",
		Exceeded:       total > 0 && remain <= 0,
		FetchedAt:      time.Now().UTC().Format(time.RFC3339),
		ProviderID:     "workbuddy",
		ExpiresAt:      expiresAt,
		ExpiringRemain: expiringRemain,
		Packages:       packages,
	}, nil
}

// AlreadyCheckedInError is a check-in-only miss. Callers should still refresh
// credits and must not write chat cooldown.
type AlreadyCheckedInError struct {
	Msg string
}

func (e AlreadyCheckedInError) Error() string {
	if strings.TrimSpace(e.Msg) == "" {
		return "workbuddy already checked in"
	}
	return e.Msg
}

func (AlreadyCheckedInError) AlreadyCheckedIn() bool { return true }

// DailyCheckin claims the upstream daily credit grant. Body is literal {}.
// Business "already checked in" returns AlreadyCheckedInError; session-dead is
// logged only here and does not Observe(auth) — keepalive owns that path.
func (c *Client) DailyCheckin(ctx context.Context, accountID string) (string, error) {
	credential, err := c.resolvedCredential(ctx, accountID)
	if err != nil {
		return "", err
	}
	var body []byte
	var status int
	for attempt := 0; ; attempt++ {
		body, status, err = c.do(ctx, accountID, http.MethodPost, credential.BillingBase()+pathDailyCheckin, []byte("{}"),
			func(h http.Header) { SetBillingHeaders(h, credential) })
		if !retryDailyCheckin(ctx, err, status, body, attempt) {
			break
		}
	}
	text := strings.TrimSpace(string(body))
	classified := Classify(status, text)
	if classified.Kind == accounts.KindAuth {
		return "", fmt.Errorf("workbuddy checkin session dead: re-login required")
	}
	if msg, ok := alreadyCheckedInMessage(status, body); ok {
		return msg, AlreadyCheckedInError{Msg: msg}
	}
	if status >= 300 {
		return "", fmt.Errorf("checkin status=%d: %s", status, text)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("checkin parse: %w", err)
	}
	msg := strings.TrimSpace(env.Msg)
	if env.Code != 0 {
		if msg == "" {
			msg = fmt.Sprintf("checkin code=%d", env.Code)
		}
		return msg, fmt.Errorf("checkin code=%d msg=%s", env.Code, msg)
	}
	if msg == "" {
		msg = "ok"
	}
	return msg, nil
}

func alreadyCheckedInMessage(status int, body []byte) (string, bool) {
	var env envelope
	msg := ""
	if json.Unmarshal(body, &env) == nil {
		msg = strings.TrimSpace(env.Msg)
		if env.Code == 0 && status < 300 {
			return "", false
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	lower := strings.ToLower(msg)
	if strings.Contains(msg, "已签到") || (strings.Contains(lower, "already") && strings.Contains(lower, "check")) {
		return msg, true
	}
	return "", false
}

func retryDailyCheckin(ctx context.Context, err error, status int, body []byte, attempt int) bool {
	requestProcessing := status == http.StatusTooManyRequests && checkinRequestProcessing(body)
	delays := dailyCheckinRetryDelays
	if requestProcessing {
		delays = dailyCheckinProcessingRetryDelays
	}
	if attempt >= len(delays) || ctx.Err() != nil {
		return false
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
	} else if status < http.StatusInternalServerError && !requestProcessing {
		return false
	}
	timer := time.NewTimer(delays[attempt])
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func checkinRequestProcessing(body []byte) bool {
	var env envelope
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &env) == nil && strings.TrimSpace(env.Msg) != "" {
		message = strings.TrimSpace(env.Msg)
	}
	lower := strings.ToLower(message)
	return strings.Contains(message, "请求处理中") ||
		strings.Contains(lower, "request is being processed") ||
		strings.Contains(lower, "request processing")
}

// Keepalive performs an expiry-aware credential check. It only exchanges a
// token when the stored credential has reached its proactive refresh window.
func (c *Client) Keepalive(ctx context.Context, accountID string) error {
	_, err := c.credential(ctx, accountID)
	return err
}

// UserResource aggregates package remain/used/total from get-user-resource.
// packages carries the per-pack expiry detail parsed from CycleEndTime.
func (c *Client) UserResource(ctx context.Context, accountID string, credential Credential) (remain, used, total int64, packages []providers.QuotaPackage, err error) {
	return c.userResourcePages(ctx, accountID, credential)
}

type resourcePackage struct {
	ResourceID        json.RawMessage `json:"ResourceId"`
	PackageName       string          `json:"PackageName"`
	PackageCode       string          `json:"PackageCode"`
	Status            *int            `json:"Status"`
	DeductionEndTime  json.RawMessage `json:"DeductionEndTime"`
	AccountAttributes []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"AccountAttributes"`

	CapacityRemain      int64  `json:"CapacityRemain"`
	CapacityUsed        int64  `json:"CapacityUsed"`
	CapacitySize        int64  `json:"CapacitySize"`
	CycleCapacityRemain int64  `json:"CycleCapacityRemain"`
	CycleCapacityUsed   int64  `json:"CycleCapacityUsed"`
	CycleCapacitySize   int64  `json:"CycleCapacitySize"`
	CycleEndTime        string `json:"CycleEndTime"`
}

// cycleEndTimeZone is the UTC+8 wall clock the upstream billing API uses for
// CycleEndTime ("2006-01-02 15:04:05").
var cycleEndTimeZone = time.FixedZone("UTC+8", 8*60*60)

// quotaPackages converts raw packs into per-pack expiry detail for the
// console. A pack with no parseable CycleEndTime keeps EndsAt zero.
func quotaPackages(packages []resourcePackage) []providers.QuotaPackage {
	out := make([]providers.QuotaPackage, 0, len(packages))
	for _, pkg := range packages {
		remain, used, size := packageRemainUsed(pkg)
		entry := providers.QuotaPackage{
			Remain: float64(remain),
			Used:   float64(used),
			Size:   float64(size),
			Unit:   "credits",
			ID:     resourceScalar(pkg.ResourceID), Source: pkg.PackageName, Status: "active",
		}
		if trimmed := strings.TrimSpace(pkg.CycleEndTime); trimmed != "" {
			if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, cycleEndTimeZone); err == nil {
				entry.EndsAt = parsed.Unix()
				entry.EndTime = trimmed
			}
		}
		if stamp := resourceDeductionEnd(pkg.DeductionEndTime); stamp > 0 {
			entry.EndsAt = stamp
			entry.EndTime = time.Unix(stamp, 0).In(cycleEndTimeZone).Format("2006-01-02 15:04:05")
			entry.ExpirySource = "deduction"
		} else if entry.EndsAt > 0 {
			entry.ExpirySource = "cycle"
		}
		for _, attr := range pkg.AccountAttributes {
			if attr.Key == "grantReason" && strings.TrimSpace(attr.Value) != "" {
				entry.Source = attr.Value
				break
			}
		}
		if entry.Source == "" {
			entry.Source = pkg.PackageCode
		}
		if entry.ExpirySource == "deduction" && entry.EndsAt > 0 && entry.EndsAt <= time.Now().Unix() {
			entry.Status = "expired"
		} else if remain <= 0 {
			entry.Status = "exhausted"
		}
		if pkg.Status != nil && *pkg.Status != 0 && *pkg.Status != 3 {
			entry.Status = "inactive"
		}
		out = append(out, entry)
	}
	return out
}

// soonestExpiry returns the earliest non-zero pack expiry and the remaining
// amount that expires at that time.
func soonestExpiry(packages []providers.QuotaPackage) (expiresAt int64, expiringRemain float64) {
	for _, pkg := range packages {
		if pkg.EndsAt <= 0 || pkg.ExpirySource == "cycle" || pkg.Remain <= 0 || pkg.Status == "expired" || pkg.Status == "inactive" || pkg.Status == "exhausted" {
			continue
		}
		if expiresAt == 0 || pkg.EndsAt < expiresAt {
			expiresAt = pkg.EndsAt
			expiringRemain = pkg.Remain
		} else if pkg.EndsAt == expiresAt {
			expiringRemain += pkg.Remain
		}
	}
	return expiresAt, expiringRemain
}

func packageRemainUsed(pkg resourcePackage) (remain, used, size int64) {
	if pkg.CycleCapacitySize > 0 {
		remain = pkg.CycleCapacityRemain
		size = pkg.CycleCapacitySize
		if remain < 0 {
			remain = 0
		}
		if remain > size {
			remain = size
		}
		used = size - remain
		if pkg.CycleCapacityUsed > used {
			used = pkg.CycleCapacityUsed
			if size >= used {
				remain = size - used
			}
		}
		return remain, used, size
	}
	if pkg.CycleCapacityRemain > 0 || pkg.CycleCapacityUsed > 0 {
		remain = pkg.CycleCapacityRemain
		used = pkg.CycleCapacityUsed
		size = pkg.CycleCapacitySize
		if remain < 0 {
			remain = 0
		}
		return remain, used, size
	}
	remain = pkg.CapacityRemain
	used = pkg.CapacityUsed
	size = pkg.CapacitySize
	if remain < 0 {
		remain = 0
	}
	if used == 0 && size > remain {
		used = size - remain
	}
	return remain, used, size
}

func aggregateUserResource(packages []resourcePackage, totalDosage int64) (remain, used, size int64) {
	for _, pkg := range packages {
		r, u, s := packageRemainUsed(pkg)
		remain += r
		used += u
		size += s
	}
	if size > 0 {
		if derived := size - remain; derived > used {
			used = derived
		}
	}
	if totalDosage > size {
		size = totalDosage
		if derived := size - remain; derived > used {
			used = derived
		}
	}
	return remain, used, size
}

// Adapter returns the provider capability bundle for registration.
func (c *Client) Adapter() providers.Adapter {
	return providers.Adapter{
		ID:                    "workbuddy",
		Credential:            credentialCodec{},
		Login:                 c,
		Chat:                  c,
		Models:                c,
		Classifier:            classifier{},
		Prober:                c,
		Checkin:               c,
		CredentialMaintenance: c,
	}
}

type credentialCodec struct{}

func (credentialCodec) Validate(payload []byte) error { return ValidateCredential(payload) }

type classifier struct{}

func (classifier) Classify(status int, body string) providers.ClassifiedError {
	return Classify(status, body)
}

func (c *Client) hasCatalogEntry(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || c == nil {
		return false
	}
	canonical := accounts.CanonicalModelID(model)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.catalog[model]; ok {
		return true
	}
	_, ok := c.catalog[canonical]
	return ok
}

// upstreamModelID returns the catalog NativeModel when the request model is a
// known catalog entry; otherwise it sends the request model unchanged. There
// is no hardcoded id rewrite table.
func (c *Client) upstreamModelID(model string) string {
	canonical := accounts.CanonicalModelID(model)
	if c != nil {
		c.mu.Lock()
		info, ok := c.catalog[model]
		if !ok {
			info, ok = c.catalog[canonical]
		}
		c.mu.Unlock()
		if ok && strings.TrimSpace(info.NativeModel) != "" {
			return info.NativeModel
		}
	}
	return model
}
