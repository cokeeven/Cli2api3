package trae

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/auth"
	"github.com/caigee-cmd/cli2api/internal/providers"
	proxyutil "github.com/caigee-cmd/cli2api/internal/proxy"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

// Store is the persistence surface the adapter needs.
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

// ModelSettingReader is optional. Missing it falls through to the legacy
// max-mode-only assertion when the request did not set IsMaxMode.
type ModelSettingReader interface {
	GetProviderModelSetting(context.Context, string, string) (accounts.ProviderModelSetting, error)
}

// ModelMaxModeReader is the Trae-only fallback used when ModelSettingReader
// is absent. Do not fold it into Store; test fakes without it must still skip.
type ModelMaxModeReader interface {
	GetProviderModelMaxMode(context.Context, string, string) (bool, error)
}

type loginPending struct {
	machineID    string
	deviceID     string
	deviceKey    string
	callbackURL  string
	codeVerifier string
	authCode     string
	trace        string
	createdAt    time.Time
	done         bool
	failed       bool
	message      string
	credential   Credential
}

type Client struct {
	store Store
	http  *http.Client

	transports proxyutil.TransportCache

	mu      sync.Mutex
	pending map[string]*loginPending
	// pendingTraces maps a login_trace_id to the pending entry that issued the
	// matching code_challenge. The callback echoes loginTraceID, so a pasted
	// callback can be matched to its exact verifier even when a newer login has
	// replaced the account's current pending entry.
	pendingTraces        map[string]*loginPending
	listener             net.Listener
	catalog              map[string]providers.ModelInfo
	credentialLocks      map[string]*sync.Mutex
	credentialRetryAfter map[string]time.Time
}

const catalogTimeout = 15 * time.Second

func NewClient(store Store) *Client {
	return &Client{
		store: store,
		http: &http.Client{
			Timeout: 120 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		pending:              map[string]*loginPending{},
		pendingTraces:        map[string]*loginPending{},
		credentialLocks:      map[string]*sync.Mutex{},
		credentialRetryAfter: map[string]time.Time{},
	}
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

func (c *Client) StartLogin(ctx context.Context, accountID string) (providers.LoginSession, error) {
	credential := Credential{Domain: DomainCN, APIHost: OAuthHost}
	if _, payload, err := c.store.LoadCredentialPayload(ctx, accountID); err == nil {
		if decoded, err := DecodeCredential(payload); err == nil {
			credential.MachineID = decoded.MachineID
			credential.DeviceID = decoded.DeviceID
			if decoded.Domain != "" {
				credential.Domain = decoded.Domain
			}
			if decoded.APIHost != "" {
				credential.APIHost = decoded.APIHost
			}
		}
	}
	credential = EnsureDevice(credential)
	credential = EnsureDeviceKey(credential)
	callbackURL, err := c.ensureCallback()
	if err != nil {
		return providers.LoginSession{}, err
	}
	trace := randomHex(8)
	verifier := ""
	if v, _, err := pkcePair(); err == nil {
		verifier = v
	}
	c.mu.Lock()
	if c.pendingTraces == nil {
		c.pendingTraces = map[string]*loginPending{}
	}
	entry := &loginPending{
		machineID:    credential.MachineID,
		deviceID:     credential.DeviceID,
		deviceKey:    credential.DevicePrivateKey,
		callbackURL:  callbackURL,
		codeVerifier: verifier,
		trace:        trace,
		createdAt:    time.Now(),
	}
	c.pending[accountID] = entry
	c.pendingTraces[trace] = entry
	c.mu.Unlock()
	authURL := buildLoginURL(credential.MachineID, credential.DeviceID, callbackURL, trace, verifier)
	return providers.LoginSession{AuthURL: authURL, State: trace}, nil
}

func (c *Client) PollLogin(ctx context.Context, accountID string) (bool, string, error) {
	c.mu.Lock()
	pending := c.pending[accountID]
	c.mu.Unlock()
	if pending == nil {
		return false, "", fmt.Errorf("login not started for account %s", accountID)
	}
	if time.Since(pending.createdAt) > loginPendingTTL {
		c.mu.Lock()
		delete(c.pending, accountID)
		c.mu.Unlock()
		return false, "", fmt.Errorf("login expired; start again")
	}
	c.mu.Lock()
	done := pending.done
	failed := pending.failed
	message := pending.message
	credential := pending.credential
	authCode := pending.authCode
	codeVerifier := pending.codeVerifier
	c.mu.Unlock()
	if failed {
		return false, "", fmt.Errorf("%s", firstNonEmpty(message, "login failed"))
	}
	if !done {
		return false, firstNonEmpty(message, "waiting for authorization"), nil
	}
	if err := c.finishCredential(ctx, accountID, credential, authCode, codeVerifier); err != nil {
		return false, "", err
	}
	c.mu.Lock()
	delete(c.pending, accountID)
	c.mu.Unlock()
	return true, "login complete", nil
}

func (c *Client) CompleteLogin(ctx context.Context, accountID, callbackURL string) error {
	info, err := ParseCallback(callbackURL)
	if err != nil {
		return err
	}
	credential := Credential{
		AccessToken:  info.AccessToken,
		RefreshToken: info.RefreshToken,
		ExpiresAt:    unixSeconds(info.ExpiresAt),
		UID:          info.UID,
		Nickname:     info.Nickname,
		EnterpriseID: info.EnterpriseID,
		Domain:       DomainCN,
		APIHost:      firstNonEmpty(info.APIHost, OAuthHost),
	}
	c.mu.Lock()
	pending := c.pending[accountID]
	// Prefer the pending entry whose challenge matches the callback's
	// loginTraceID, so a pasted callback is paired with the verifier that
	// actually minted its code even if a newer login replaced the account slot.
	if info.Trace != "" {
		if traced := c.pendingTraces[info.Trace]; traced != nil {
			pending = traced
		}
	}
	c.mu.Unlock()
	codeVerifier := ""
	if pending != nil {
		if pending.done {
			c.mu.Lock()
			delete(c.pending, accountID)
			c.mu.Unlock()
			return nil
		}
		credential.MachineID = pending.machineID
		credential.DeviceID = pending.deviceID
		credential.DevicePrivateKey = pending.deviceKey
		codeVerifier = pending.codeVerifier
	} else if _, payload, err := c.store.LoadCredentialPayload(ctx, accountID); err == nil {
		if decoded, err := DecodeCredential(payload); err == nil {
			if decoded.Ready() {
				return nil
			}
			credential.MachineID = decoded.MachineID
			credential.DeviceID = decoded.DeviceID
		}
	}
	if err := c.finishCredential(ctx, accountID, credential, info.AuthCode, codeVerifier); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.pending, accountID)
	if pending != nil && pending.trace != "" {
		delete(c.pendingTraces, pending.trace)
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) finishCredential(ctx context.Context, accountID string, credential Credential, authCode, codeVerifier string) error {
	switch {
	case strings.TrimSpace(authCode) != "":
		// Exchange the callback's authorization code with the PKCE verifier.
		refreshed, err := c.ExchangeAuthCode(ctx, accountID, credential, authCode, codeVerifier)
		if err != nil {
			return err
		}
		credential = refreshed
	case strings.TrimSpace(credential.RefreshToken) != "":
		refreshed, err := c.ExchangeToken(ctx, accountID, credential)
		if err != nil {
			return err
		}
		credential = refreshed
	}
	if strings.TrimSpace(credential.UID) == "" && strings.TrimSpace(credential.AccessToken) != "" {
		info, err := c.GetUserInfo(ctx, accountID, credential)
		if err == nil {
			credential.UID = info.UID
			if info.Nickname != "" {
				credential.Nickname = info.Nickname
			}
			if info.EnterpriseID != "" {
				credential.EnterpriseID = info.EnterpriseID
			}
		}
	}
	credential = EnsureDevice(credential)
	credential = EnsureDeviceKey(credential)
	payload, err := credential.Encode()
	if err != nil {
		return err
	}
	if err := c.store.SaveCredentialPayload(ctx, accountID, CredentialFormat, payload); err != nil {
		return err
	}
	_ = c.store.Observe(ctx, accountID, credential.UID, "ready", "", "")
	return nil
}

func (c *Client) ensureCallback() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listener != nil {
		addr, ok := c.listener.Addr().(*net.TCPAddr)
		if ok {
			return fmt.Sprintf("http://127.0.0.1:%d%s", addr.Port, pathCallback), nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("trae callback listen: %w", err)
	}
	c.listener = ln
	auth.ServeLoopback(ln, pathCallback, "Trae", c.acceptCallback)
	addr := ln.Addr().(*net.TCPAddr)
	return fmt.Sprintf("http://127.0.0.1:%d%s", addr.Port, pathCallback), nil
}

func (c *Client) acceptCallback(ctx context.Context, rawURL string) error {
	info, err := ParseCallback(rawURL)
	if err != nil {
		c.markPendingFailed(err.Error())
		return err
	}
	credential := Credential{
		AccessToken:  info.AccessToken,
		RefreshToken: info.RefreshToken,
		ExpiresAt:    unixSeconds(info.ExpiresAt),
		UID:          info.UID,
		Nickname:     info.Nickname,
		EnterpriseID: info.EnterpriseID,
		Domain:       DomainCN,
		APIHost:      OAuthHost,
	}
	c.mu.Lock()
	for _, pending := range c.pending {
		if pending.done || pending.failed {
			continue
		}
		credential.MachineID = pending.machineID
		credential.DeviceID = pending.deviceID
		credential.DevicePrivateKey = pending.deviceKey
		pending.credential = credential
		pending.authCode = info.AuthCode
		pending.done = true
		pending.message = "authorization received"
		break
	}
	c.mu.Unlock()
	return nil
}

func (c *Client) markPendingFailed(message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, pending := range c.pending {
		if pending.done || pending.failed {
			continue
		}
		pending.failed = true
		pending.message = message
		return
	}
}

type callbackInfo struct {
	AuthCode     string
	RefreshToken string
	AccessToken  string
	UID          string
	Nickname     string
	EnterpriseID string
	ExpiresAt    int64
	APIHost      string
	Trace        string
}

func ParseCallback(rawURL string) (callbackInfo, error) {
	if strings.TrimSpace(rawURL) == "" {
		return callbackInfo{}, fmt.Errorf("empty callback url")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return callbackInfo{}, fmt.Errorf("parse callback url: %w", err)
	}
	query := parsed.Query()
	info := callbackInfo{
		AuthCode:     firstNonEmpty(query.Get("code"), query.Get("authCode"), query.Get("auth_code")),
		RefreshToken: firstNonEmpty(query.Get("refreshToken"), query.Get("refresh_token")),
		AccessToken:  firstNonEmpty(query.Get("accessToken"), query.Get("userJwt")),
		Trace:        firstNonEmpty(query.Get("loginTraceID"), query.Get("login_trace_id")),
	}
	// The authorization code is delivered inside a JSON authCodeInfo parameter:
	// {"AuthCode":"...","ExpireAt":...,"ExpireDuration":...}.
	if info.AuthCode == "" {
		if raw := firstNonEmpty(query.Get("authCodeInfo"), query.Get("auth_code_info")); raw != "" {
			var nested struct {
				AuthCode string `json:"AuthCode"`
				Code     string `json:"code"`
			}
			if json.Unmarshal([]byte(raw), &nested) == nil {
				info.AuthCode = firstNonEmpty(nested.AuthCode, nested.Code)
			}
		}
	}
	// The callback also carries host / userRegion hints; prefer the callback host
	// so the code exchange targets the region the browser logged into.
	if host := strings.TrimSpace(query.Get("host")); host != "" {
		host = strings.TrimRight(host, "/")
		if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
			info.APIHost = host
		}
	}
	if userInfo := query.Get("userInfo"); userInfo != "" {
		var nested struct {
			UserID       string `json:"UserID"`
			ScreenName   string `json:"ScreenName"`
			TenantID     string `json:"TenantID"`
			UID          string `json:"uid"`
			Nickname     string `json:"nickname"`
			EnterpriseID string `json:"enterpriseId"`
		}
		if json.Unmarshal([]byte(userInfo), &nested) == nil {
			info.UID = firstNonEmpty(nested.UserID, nested.UID)
			info.Nickname = firstNonEmpty(nested.ScreenName, nested.Nickname)
			info.EnterpriseID = firstNonEmpty(nested.TenantID, nested.EnterpriseID)
		}
	}
	if userJWT := query.Get("userJwt"); userJWT != "" {
		var jwt struct {
			Token         string `json:"Token"`
			RefreshToken  string `json:"RefreshToken"`
			TokenExpireAt int64  `json:"TokenExpireAt"`
		}
		if json.Unmarshal([]byte(userJWT), &jwt) == nil {
			if info.RefreshToken == "" {
				info.RefreshToken = jwt.RefreshToken
			}
			if jwt.Token != "" {
				info.AccessToken = jwt.Token
			}
			if jwt.TokenExpireAt != 0 {
				info.ExpiresAt = jwt.TokenExpireAt
			}
		}
	}
	if info.RefreshToken == "" && info.AccessToken == "" && info.AuthCode == "" {
		return callbackInfo{}, fmt.Errorf("callback missing code, refreshToken and userJwt.Token")
	}
	return info, nil
}

func buildLoginURL(machineID, deviceID, callbackURL, trace, codeVerifier string) string {
	values := url.Values{}
	values.Set("login_version", "1")
	values.Set("auth_from", AuthFrom)
	values.Set("login_channel", "native_ide")
	values.Set("plugin_version", PluginVersion)
	values.Set("auth_type", "local")
	values.Set("client_id", ClientID)
	values.Set("redirect", "0")
	values.Set("login_trace_id", trace)
	values.Set("auth_callback_url", callbackURL)
	values.Set("machine_id", machineID)
	values.Set("device_id", deviceID)
	values.Set("x_device_id", deviceID)
	values.Set("x_machine_id", machineID)
	values.Set("x_device_brand", "PC")
	values.Set("x_device_type", "PC")
	values.Set("x_os_version", "1.0")
	values.Set("x_env", "")
	values.Set("x_app_version", IdeVersion)
	values.Set("x_app_type", "stable")
	if codeVerifier == "" {
		codeVerifier, _ = pkceVerifier()
	}
	if codeVerifier != "" {
		values.Set("code_challenge", pkceChallenge(codeVerifier))
		values.Set("code_challenge_method", "S256")
	}
	return ConsoleHost + pathAuthorization + "?" + values.Encode()
}

func (c *Client) ExchangeToken(ctx context.Context, accountID string, credential Credential) (Credential, error) {
	if strings.TrimSpace(credential.RefreshToken) == "" {
		return credential, fmt.Errorf("no refreshToken")
	}
	// Trae >= 2026-09 requires a device proof on the new v3 exchange endpoint.
	// Prefer the proof-bearing exchange; fall back to the legacy endpoint only
	// when the credential lacks a device key (accounts created before the PKCE
	// switch), which the legacy client still accepts.
	clientID := c.refreshClientID(credential)
	if strings.TrimSpace(credential.DevicePrivateKey) != "" {
		reqBody := map[string]any{
			"ClientID":     clientID,
			"ClientSecret": "",
			"RefreshToken": credential.RefreshToken,
			"IDEVersion":   IdeVersion,
			"DeviceInfo":   deviceInfoFor(credential),
		}
		if proof, err := deviceProof(credential.DevicePrivateKey, pathExchangeCode, clientID, credential.RefreshToken); err == nil {
			reqBody["DeviceProof"] = proof
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return credential, err
		}
		payload, status, err := c.do(ctx, accountID, http.MethodPost, credential.AuthBase()+pathExchangeCode, body, SetOAuthHeaders)
		if err == nil && status < 300 {
			return c.applyTokenResult(credential, payload)
		}
		// Fall through to the legacy endpoint on any failure; the legacy
		// endpoint may still accept accounts minted before the switch.
	}
	body, err := json.Marshal(map[string]any{
		"ClientID":     clientID,
		"RefreshToken": credential.RefreshToken,
		"ClientSecret": "-",
		"UserID":       "",
	})
	if err != nil {
		return credential, err
	}
	payload, status, err := c.do(ctx, accountID, http.MethodPost, credential.AuthBase()+pathExchange, body, SetOAuthHeaders)
	if err != nil {
		return credential, err
	}
	if status >= 300 {
		return credential, classifiedError(status, payload)
	}
	return c.applyTokenResult(credential, payload)
}

// applyTokenResult parses the shared token response shape and fills the
// credential's access/refresh tokens and expiry timestamps.
func (c *Client) applyTokenResult(credential Credential, payload []byte) (Credential, error) {
	refreshedAt := time.Now()
	var env struct {
		Result struct {
			Token               string `json:"Token"`
			TokenExpireAt       int64  `json:"TokenExpireAt"`
			TokenExpireDuration int64  `json:"TokenExpireDuration"`
			RefreshToken        string `json:"RefreshToken"`
			RefreshExpireAt     int64  `json:"RefreshExpireAt"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return credential, fmt.Errorf("exchange token parse: %w", err)
	}
	if strings.TrimSpace(env.Result.Token) == "" {
		return credential, fmt.Errorf("refresh_failed: no token in response — re-login required")
	}
	credential.AccessToken = env.Result.Token
	if env.Result.RefreshToken != "" {
		credential.RefreshToken = env.Result.RefreshToken
	}
	if env.Result.TokenExpireAt != 0 {
		credential.ExpiresAt = unixSeconds(env.Result.TokenExpireAt)
	} else if env.Result.TokenExpireDuration > 0 {
		credential.ExpiresAt = refreshedAt.Add(time.Duration(env.Result.TokenExpireDuration) * time.Second).Unix()
	}
	if credential.ExpiresAt > 0 {
		remaining := time.Until(time.Unix(credential.ExpiresAt, 0))
		if remaining <= 0 {
			credential.RefreshAfter = refreshedAt.Unix()
		} else {
			lead := refreshLead
			if half := remaining / 2; half < lead {
				lead = half
			}
			credential.RefreshAfter = time.Unix(credential.ExpiresAt, 0).Add(-lead).Unix()
		}
	} else {
		// Unknown expiry must not turn every concurrent request into another
		// rotating-token exchange. Retry after a short bounded interval.
		credential.RefreshAfter = refreshedAt.Add(5 * time.Minute).Unix()
	}
	if env.Result.RefreshExpireAt != 0 {
		credential.RefreshExpiresAt = unixSeconds(env.Result.RefreshExpireAt)
	}
	return credential, nil
}

// refreshClientID is the OAuth client used to refresh the credential's token.
// Normally the login client; accounts created before the PKCE switch record the
// legacy SOLO client that minted their refresh token, because refresh tokens are
// bound to the client that issued them.
func (c *Client) refreshClientID(credential Credential) string {
	if id := strings.TrimSpace(credential.RefreshClientID); id != "" {
		return id
	}
	return ClientID
}

// deviceInfo builds the DeviceInfo object shared by both the auth-code and
// refresh exchanges, including the device public key derived from the stored
// private key.
func deviceInfoFor(credential Credential) map[string]any {
	info := map[string]any{
		"DeviceID":      credential.DeviceID,
		"MachineID":     credential.MachineID,
		"PlatformCode":  "IDE_PC",
		"DeviceType":    "PC",
		"DeviceName":    "cli2api",
		"DeviceModel":   "",
		"ClientVersion": IdeVersion,
		"DeviceBrand":   "",
		"OSInfo":        "Windows",
		"OSVersion":     OSVersion,
	}
	if pubKey := devicePublicKeyPEM(credential.DevicePrivateKey); pubKey != "" {
		info["DevicePublicKey"] = pubKey
	}
	return info
}

// ExchangeAuthCode performs the PKCE authorization-code exchange:
// POST /trae/api/v3/oauth/ExchangeToken with the callback's code and the
// matching code verifier. The response carries the same token fields as the
// refresh-token exchange, so it fills the same Credential.
func (c *Client) ExchangeAuthCode(ctx context.Context, accountID string, credential Credential, authCode, codeVerifier string) (Credential, error) {
	if strings.TrimSpace(authCode) == "" {
		return credential, fmt.Errorf("no auth code")
	}
	reqBody := map[string]any{
		"ClientID":     ClientID,
		"AuthCode":     authCode,
		"CodeVerifier": codeVerifier,
		"IDEVersion":   IdeVersion,
		"DeviceInfo":   deviceInfoFor(credential),
	}
	// Trae >= 2026-09 requires a device proof (ECDSA P-256 signature) or the
	// endpoint answers 20405 "Device proof required".
	if proof, err := deviceProof(credential.DevicePrivateKey, pathExchangeCode, ClientID, authCode); err == nil {
		reqBody["DeviceProof"] = proof
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return credential, err
	}
	payload, status, err := c.do(ctx, accountID, http.MethodPost, credential.AuthBase()+pathExchangeCode, body, SetOAuthHeaders)
	if err != nil {
		return credential, err
	}
	if status >= 300 {
		return credential, classifiedError(status, payload)
	}
	var env struct {
		Result struct {
			Token               string `json:"Token"`
			TokenExpireAt       int64  `json:"TokenExpireAt"`
			TokenExpireDuration int64  `json:"TokenExpireDuration"`
			RefreshToken        string `json:"RefreshToken"`
			RefreshExpireAt     int64  `json:"RefreshExpireAt"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return credential, fmt.Errorf("code exchange parse: %w", err)
	}
	if strings.TrimSpace(env.Result.Token) == "" {
		return credential, fmt.Errorf("code_exchange_failed: no token in response")
	}
	credential.AccessToken = env.Result.Token
	if env.Result.RefreshToken != "" {
		credential.RefreshToken = env.Result.RefreshToken
	}
	if env.Result.TokenExpireAt != 0 {
		credential.ExpiresAt = unixSeconds(env.Result.TokenExpireAt)
	} else if env.Result.TokenExpireDuration > 0 {
		credential.ExpiresAt = time.Now().Add(time.Duration(env.Result.TokenExpireDuration) * time.Second).Unix()
	}
	if env.Result.RefreshExpireAt != 0 {
		credential.RefreshExpiresAt = unixSeconds(env.Result.RefreshExpireAt)
	}
	return credential, nil
}

type userInfo struct {
	UID          string
	Nickname     string
	EnterpriseID string
}

func (c *Client) GetUserInfo(ctx context.Context, accountID string, credential Credential) (userInfo, error) {
	body, err := json.Marshal(map[string]any{
		"ReqSource":  "IDE",
		"IDEVersion": IdeVersion,
	})
	if err != nil {
		return userInfo{}, err
	}
	payload, status, err := c.do(ctx, accountID, http.MethodPost, credential.AuthBase()+pathUserInfo, body, func(h http.Header) {
		SetOAuthHeaders(h)
		if credential.AccessToken != "" {
			h.Set("X-Cloudide-Token", credential.AccessToken)
		}
	})
	if err != nil {
		return userInfo{}, err
	}
	if status >= 300 {
		return userInfo{}, classifiedError(status, payload)
	}
	var env struct {
		Result struct {
			UserID       string `json:"UserID"`
			ScreenName   string `json:"ScreenName"`
			EnterpriseID string `json:"EnterpriseID"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return userInfo{}, fmt.Errorf("user info parse: %w", err)
	}
	return userInfo{
		UID:          env.Result.UserID,
		Nickname:     env.Result.ScreenName,
		EnterpriseID: env.Result.EnterpriseID,
	}, nil
}

func (c *Client) credential(ctx context.Context, accountID string) (Credential, error) {
	format, payload, credential, err := c.loadCredential(ctx, accountID)
	if err != nil {
		return Credential{}, err
	}
	now := time.Now()
	if credential.refreshExpired(now) {
		return c.useTraeCredentialFallback(ctx, accountID, credential, now, fmt.Errorf("trae refresh token expired; re-login required"), true)
	}
	if !credential.needsRefresh(now) {
		return credential, nil
	}
	lock := c.credentialLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	format, payload, credential, err = c.loadCredential(ctx, accountID)
	if err != nil {
		return Credential{}, err
	}
	now = time.Now()
	if credential.refreshExpired(now) {
		return c.useTraeCredentialFallback(ctx, accountID, credential, now, fmt.Errorf("trae refresh token expired; re-login required"), true)
	}
	if !credential.needsRefresh(now) {
		return credential, nil
	}
	account, err := c.store.Get(ctx, accountID)
	if err != nil {
		return Credential{}, fmt.Errorf("load Trae account before credential refresh: %w", err)
	}
	if c.credentialRefreshCoolingDown(accountID, now) {
		return c.useTraeCredentialFallback(ctx, accountID, credential, now, fmt.Errorf("credential refresh is cooling down"), false)
	}
	refreshed, err := c.ExchangeToken(ctx, accountID, credential)
	if err != nil {
		c.recordCredentialRefreshFailure(accountID, err)
		return c.useTraeCredentialFallback(ctx, accountID, credential, now, err, isCredentialAuthError(err))
	}
	encoded, err := refreshed.Encode()
	if err != nil {
		return Credential{}, err
	}
	saved, err := c.saveCredentialAfterRefreshIfCurrent(ctx, accountID, format, payload, encoded, account.CredentialGeneration)
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

func (c *Client) saveCredentialAfterRefreshIfCurrent(ctx context.Context, accountID, expectedFormat string, expectedPayload, payload []byte, credentialGeneration int64) (bool, error) {
	store, ok := c.store.(providers.CredentialRefreshCASStore)
	if !ok {
		return false, fmt.Errorf("trae credential store does not support atomic refresh compare-and-swap")
	}
	return store.SaveCredentialPayloadAfterRefreshIfCurrent(ctx, accountID, expectedFormat, expectedPayload, CredentialFormat, payload, credentialGeneration)
}

func (c *Client) useTraeCredentialFallback(ctx context.Context, accountID string, credential Credential, now time.Time, refreshErr error, authFailure bool) (Credential, error) {
	if credential.ExpiresAt > now.Unix() {
		return credential, nil
	}
	if authFailure {
		_ = c.store.Observe(ctx, accountID, credential.UID, "login_required", "credential expired; re-login required", accounts.KindAuth)
		return Credential{}, &providers.Error{Kind: accounts.KindAuth, Status: http.StatusUnauthorized, Message: "trae credential expired; re-login required"}
	}
	var classified *providers.Error
	if errors.As(refreshErr, &classified) {
		return Credential{}, fmt.Errorf("trae access token expired and refresh failed: %w", classified)
	}
	return Credential{}, &providers.Error{Kind: accounts.KindUnavailable, Status: http.StatusServiceUnavailable, Message: "trae credential expired and refresh is temporarily unavailable"}
}

func isCredentialAuthError(err error) bool {
	var classified *providers.Error
	return errors.As(err, &classified) && classified.Kind == accounts.KindAuth ||
		strings.Contains(strings.ToLower(err.Error()), "refresh_failed")
}

func (c *Client) Models(ctx context.Context, accountID string) ([]providers.ModelInfo, error) {
	credential, err := c.credential(ctx, accountID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	out, err := c.fetchCatalogScene(ctx, accountID, credential, PrimaryScene)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("trae model catalog returned no models")
	}
	// Merge the secondary scene so models the primary scene hides (e.g.
	// kimi-k2.6) become selectable. Each model keeps the scene it is served
	// through; a failure here must not sink the primary catalog.
	if SecondaryScene != "" && SecondaryScene != PrimaryScene {
		if extra, err := c.fetchCatalogScene(ctx, accountID, credential, SecondaryScene); err == nil {
			out = mergeCatalogModels(out, extra)
		}
	}
	c.rememberCatalog(out)
	return out, nil
}

// fetchCatalogScene fetches and parses one catalog scene (function). It returns
// an empty slice (no error) when the scene yields nothing usable.
func (c *Client) fetchCatalogScene(ctx context.Context, accountID string, credential Credential, function string) ([]providers.ModelInfo, error) {
	body, err := json.Marshal(map[string]any{
		"function":            function,
		"config_names":        nil,
		"need_prompt":         false,
		"current_config_info": nil,
		"poly_prompt":         true,
		"mode_type":           nil,
		"agent_type":          nil,
	})
	if err != nil {
		return nil, err
	}
	payload, status, err := c.do(ctx, accountID, http.MethodPost, credential.ChatBase()+pathModels, body,
		func(h http.Header) { SetCatalogHeaders(h, credential) })
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, classifiedError(status, payload)
	}
	out, err := parseCatalogModels(payload, function)
	if err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	return out, nil
}

func (c *Client) rememberCatalog(models []providers.ModelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog = make(map[string]providers.ModelInfo, len(models))
	for _, model := range models {
		c.catalog[model.NativeModel] = model
		c.catalog[strings.ToLower(model.NativeModel)] = model
	}
}

func (c *Client) capsFor(model string) providers.ModelCapabilities {
	if info, ok := c.lookupCatalogModel(model); ok {
		return info.Capabilities
	}
	return providers.ModelCapabilities{}
}

// sceneFor returns the scene a chat for this model must be sent under. Trae
// serves the whole catalog through chat_v3 by default; only models that scene
// does not list (e.g. kimi-k2.6) fall back to solo_work_lite. A model missing
// from the catalog entirely uses PrimaryScene.
func (c *Client) sceneFor(model string) string {
	if info, ok := c.lookupCatalogModel(model); ok && info.Scene != "" {
		return info.Scene
	}
	return PrimaryScene
}

// lookupCatalogModel resolves a request/console model id to its catalog entry.
// Trae config_name is mixed-case; callers may send the console canonical form
// (lowercase, _ folded to -), so both join keys are tried.
func (c *Client) lookupCatalogModel(model string) (providers.ModelInfo, bool) {
	model = strings.TrimSpace(model)
	c.mu.Lock()
	defer c.mu.Unlock()
	if info, ok := c.catalog[model]; ok {
		return info, true
	}
	info, ok := c.catalog[accounts.CanonicalModelID(model)]
	return info, ok
}

// settingModelKey mirrors control.ModelContextKey so provider settings saved by
// the console are found again at chat time. Trae config_name is mixed-case and
// may contain underscores; both sides must canonicalize identically.
func settingModelKey(model string) string {
	return accounts.CanonicalModelID(model)
}

func (c *Client) chatRequest(ctx context.Context, credential Credential, req translate.ChatRequest, accountID string) (*http.Request, providers.ResolvedChat, error) {
	if err := validateCallerResponseFormat(req); err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	if err := translate.ValidateChatToolHistory(req); err != nil {
		return nil, providers.ResolvedChat{}, &providers.Error{
			Kind: "invalid_request", Status: 400, Code: "invalid_tool_history",
			Message: "Trae 工具历史无效：" + err.Error() + "；网关不会自动补全参数或执行不完整调用。",
		}
	}
	payload, err := json.Marshal(map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"tools":       req.Tools,
		"tool_choice": req.ToolChoice,
	})
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	scene := c.sceneFor(req.Model)
	rewritten := PrepareBody(payload, scene)
	resolved := providers.ResolvedChat{}
	var obj map[string]any
	if err := json.Unmarshal(rewritten, &obj); err == nil {
		maxMode := false
		storedLevel := ""
		if req.IsMaxMode != nil {
			maxMode = *req.IsMaxMode
		}
		caps := c.capsFor(req.Model)
		if setter, ok := c.store.(ModelSettingReader); ok {
			if stored, err := setter.GetProviderModelSetting(ctx, "trae", settingModelKey(req.Model)); err == nil {
				if req.IsMaxMode == nil {
					maxMode = stored.MaxMode
				}
				storedLevel = stored.ReasoningEffort
			}
		} else if req.IsMaxMode == nil {
			if maxSetter, ok := c.store.(ModelMaxModeReader); ok {
				if stored, err := maxSetter.GetProviderModelMaxMode(ctx, "trae", settingModelKey(req.Model)); err == nil {
					maxMode = stored
				}
			}
		}
		// Caps may be empty because the catalog has not been fetched (fresh
		// process, first request) or the model is unknown. Only the unknown
		// case is unrecoverable; refresh once before deciding.
		if len(caps.ReasoningOptions) == 0 && !caps.MaxMode && accountID != "" {
			_, _ = c.Models(ctx, accountID)
			caps = c.capsFor(req.Model)
		}
		resolved.OutputBudgetEvidence = outputBudgetEvidence(req.Model, scene, caps.MaxOutput)
		if err := validateCallerReasoning(req, caps); err != nil {
			return nil, providers.ResolvedChat{}, err
		}
		if maxMode && !caps.MaxMode {
			log.Printf("trae model %q: max mode requested but catalog says unsupported; sending default context window", req.Model)
		}
		outputLimit, outputSource, err := providers.ResolveOutputLimit(req, caps, maxMode)
		if err != nil {
			return nil, providers.ResolvedChat{}, err
		}
		if outputLimit != nil {
			obj["max_tokens"] = *outputLimit
		} else {
			delete(obj, "max_tokens")
		}
		resolved.OutputLimit = outputLimit
		resolved.OutputLimitSource = outputSource
		applySoloChatFields(obj, req, maxMode, storedLevel, caps)
		resolved.ReasoningLevel = resolvedReasoningLevel(req, storedLevel, caps)
		if encoded, err := json.Marshal(obj); err == nil {
			rewritten = encoded
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		credential.ChatBase()+pathChat, bytes.NewReader(rewritten))
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	SetChatHeaders(httpReq.Header, credential)
	return httpReq, resolved, nil
}

func (c *Client) ChatNonStream(ctx context.Context, accountID string, req translate.ChatRequest) (providers.ChatOutcome, error) {
	if err := validateCallerResponseFormat(req); err != nil {
		return providers.ChatOutcome{}, err
	}
	credential, err := c.credential(ctx, accountID)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	httpReq, resolved, err := c.chatRequest(ctx, credential, req, accountID)
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
		readErr = fmt.Errorf("trae response exceeds 16 MiB")
	}
	if resp.StatusCode >= 300 {
		return resolved.OutcomeMetadata(), providers.WithRetryAfterHeader(classifiedError(resp.StatusCode, body), resp.Header.Get("Retry-After"))
	}
	aggregate, aggregateErr := Aggregate(bytes.NewReader(body))
	outcome, err := outcomeFromAggregate(aggregate, req.Model)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	outcome.ReasoningLevel = resolved.ReasoningLevel
	outcome.OutputLimit = resolved.OutputLimit
	outcome.OutputLimitSource = resolved.OutputLimitSource
	outcome.OutputBudgetEvidence = resolved.OutputBudgetEvidence
	return outcome, providers.WithRetryAfterHeader(errors.Join(readErr, aggregateErr), resp.Header.Get("Retry-After"))
}

func (c *Client) ChatStream(ctx context.Context, accountID string, req translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	if err := validateCallerResponseFormat(req); err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	credential, err := c.credential(ctx, accountID)
	if err != nil {
		return nil, providers.ResolvedChat{}, err
	}
	httpReq, resolved, err := c.chatRequest(ctx, credential, req, accountID)
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
	streamResp, err := rewriteSoloStream(resp.Body, req.Model)
	if err != nil {
		return nil, resolved, providers.WithRetryAfterHeader(err, resp.Header.Get("Retry-After"))
	}
	streamResp.Body = providers.CarryChatObservation(streamResp.Body, resp.Body, resp.Header.Get("Retry-After"))
	for _, key := range []string{"X-Request-ID", "X-Upstream-Request-ID", "Retry-After"} {
		if value := resp.Header.Get(key); value != "" {
			streamResp.Header.Set(key, value)
		}
	}
	return streamResp, resolved, nil
}

func outcomeFromAggregate(aggregate map[string]any, fallbackModel string) (providers.ChatOutcome, error) {
	raw, err := json.Marshal(aggregate)
	if err != nil {
		return providers.ChatOutcome{}, err
	}
	var parsed struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens             *int   `json:"prompt_tokens"`
			CompletionTokens         *int   `json:"completion_tokens"`
			CacheReadTokens          *int   `json:"cache_read_tokens"`
			CacheWriteTokens         *int   `json:"cache_write_tokens"`
			PromptCacheHitTokens     *int   `json:"prompt_cache_hit_tokens"`
			CacheReadInputTokens     *int   `json:"cache_read_input_tokens"`
			CacheCreationInputTokens *int   `json:"cache_creation_input_tokens"`
			Source                   string `json:"source"`
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
	out := providers.ChatOutcome{UsageSource: "unknown", FinishReason: "stop", FinishReasonSource: "adapter_default", Model: fallbackModel}
	if parsed.Model != "" {
		out.Model = parsed.Model
	}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
		out.Reasoning = parsed.Choices[0].Message.ReasoningContent
		out.ToolCalls = parsed.Choices[0].Message.ToolCalls
		if parsed.Choices[0].FinishReason != "" {
			out.FinishReason = parsed.Choices[0].FinishReason
			out.FinishReasonSource = "upstream"
		}
	}
	if parsed.Usage.PromptTokens != nil {
		out.PromptTokens = *parsed.Usage.PromptTokens
	}
	if parsed.Usage.CompletionTokens != nil {
		out.CompletionTokens = *parsed.Usage.CompletionTokens
	}
	out.ReportedPromptTokens, out.ReportedCompletionTokens = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	out.UsageExtensions = providers.NumericUsageExtensions(aggregate["usage"])
	if parsed.Usage.PromptTokens != nil && parsed.Usage.CompletionTokens != nil {
		out.UsageSource = firstNonEmpty(parsed.Usage.Source, "upstream")
	}
	out.CacheReadTokens = providers.FirstIntPointer(parsed.Usage.CacheReadTokens, parsed.Usage.PromptDetails.CachedTokens, parsed.Usage.PromptCacheHitTokens, parsed.Usage.CacheReadInputTokens)
	out.CachedTokens = out.CacheReadTokens
	out.CacheWriteTokens = providers.FirstIntPointer(parsed.Usage.CacheWriteTokens, parsed.Usage.CacheCreationInputTokens)
	return out, nil
}

func classifiedError(status int, body []byte) error {
	return wrapClassified(Classify(status, string(body)), extractCode(string(body)))
}

func wrapClassified(classified providers.ClassifiedError, code string) error {
	return &providers.Error{
		Kind:      classified.Kind,
		Status:    classified.Status,
		Message:   classified.Message,
		Code:      firstNonEmpty(code, classified.Code),
		Scope:     classified.Scope,
		RetrySafe: classified.RetrySafe,
	}
}

func extractCode(body string) string {
	var env struct {
		Code any `json:"code"`
	}
	if json.Unmarshal([]byte(body), &env) != nil || env.Code == nil {
		return ""
	}
	switch v := env.Code.(type) {
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("%.0f", v)
	case string:
		return strings.TrimSpace(v)
	default:
		text := strings.TrimSpace(fmt.Sprint(v))
		if text == "" || text == "<nil>" {
			return ""
		}
		return text
	}
}

// Classify maps Trae HTTP/error bodies onto the internal taxonomy.
func Classify(status int, body string) providers.ClassifiedError {
	text := strings.ToLower(body)
	code := extractCode(body)
	if accounts.IsPromptLimitText(body) {
		return providers.ClassifiedError{Kind: accounts.KindInvalidRequest, Status: 400, Message: strings.TrimSpace(body)}
	}
	switch {
	case code == "1001" || status == 401 || strings.Contains(text, "unauthorized"):
		return providers.ClassifiedError{Kind: accounts.KindAuth, Status: 401, Message: firstNonEmpty(strings.TrimSpace(body), "session dead; re-login required")}
	case code == "1005" || (strings.Contains(text, "1005") && strings.Contains(text, "plan")):
		return providers.ClassifiedError{Kind: accounts.KindQuota, Status: 429, Message: firstNonEmpty(strings.TrimSpace(body), "plan limit")}
	case code == "4008":
		return providers.ClassifiedError{Kind: accounts.KindQuota, Status: 429, Message: firstNonEmpty(strings.TrimSpace(body), "solo credits exhausted")}
	case code == "4001":
		return providers.ClassifiedError{Kind: accounts.KindInvalidRequest, Status: 400, Message: firstNonEmpty(strings.TrimSpace(body), "model or ide version mismatch")}
	case code == "3004":
		return providers.ClassifiedError{Kind: accounts.KindRateLimit, Status: 429, Message: firstNonEmpty(strings.TrimSpace(body), "upstream rate limit")}
	case code == "4011":
		return providers.ClassifiedError{Kind: accounts.KindRateLimit, Status: 429, Message: firstNonEmpty(strings.TrimSpace(body), "hard rate limit")}
	case status == 429 || strings.Contains(text, "too many requests"):
		return providers.ClassifiedError{Kind: accounts.KindRateLimit, Status: 429, Message: strings.TrimSpace(body)}
	case status == 404:
		return providers.ClassifiedError{Kind: accounts.KindUnavailable, Status: 404, Message: strings.TrimSpace(body)}
	case status == 400 || accounts.IsPromptLimitText(body) || accounts.IsInvalidRequestText(body):
		return providers.ClassifiedError{Kind: accounts.KindInvalidRequest, Status: firstNonEmptyStatus(status, 400), Message: strings.TrimSpace(body)}
	case status >= 500:
		return providers.ClassifiedError{Kind: accounts.KindUnavailable, Status: status, Message: strings.TrimSpace(body)}
	}
	if code != "" && code != "0" && status < 300 {
		if code == "9074" {
			return providers.ClassifiedError{}
		}
		return providers.ClassifiedError{Kind: accounts.KindUnavailable, Status: 502, Message: strings.TrimSpace(body)}
	}
	return providers.ClassifiedError{}
}

func firstNonEmptyStatus(status, fallback int) int {
	if status >= 400 {
		return status
	}
	return fallback
}

func (c *Client) Probe(ctx context.Context, accountID string) (providers.AccountHealth, error) {
	credential, err := c.credential(ctx, accountID)
	if err != nil {
		if errors.Is(err, accounts.ErrAccountNotFound) || errors.Is(err, accounts.ErrSecretNotFound) {
			if _, accountErr := c.store.Get(ctx, accountID); accountErr == nil {
				return providers.AccountHealth{
					LastError:     "Trae 本地登录凭据缺失，请使用账号卡片的登录入口完成登录。",
					LastErrorKind: accounts.KindAuth,
				}, nil
			}
		}
		var classified *providers.Error
		kind := accounts.KindUnavailable
		if errors.As(err, &classified) && classified.Kind != "" {
			kind = classified.Kind
		}
		return providers.AccountHealth{LastError: err.Error(), LastErrorKind: kind}, nil
	}
	if !credential.Ready() {
		msg := "trae credential incomplete; re-login required"
		return providers.AccountHealth{UID: credential.UID, LastError: msg, LastErrorKind: accounts.KindAuth}, nil
	}
	return providers.AccountHealth{
		Ready: true,
		Hot:   true,
		UID:   credential.UID,
	}, nil
}

func (c *Client) Quota(ctx context.Context, accountID string) (*providers.QuotaInfo, error) {
	credential, err := c.credential(ctx, accountID)
	if err != nil {
		return nil, err
	}
	general, _, err := c.UserEntUsage(ctx, accountID, credential)
	if err != nil {
		return nil, err
	}
	// Only the General bucket is surfaced. The Work-only bucket is parsed (see
	// parseEntitlementBuckets) but deliberately not summed in or shown.
	remain, used, total := general.remain, general.used, general.total
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
	return &providers.QuotaInfo{
		Used:       float64(used),
		Total:      float64(total),
		Remaining:  float64(remain),
		Percentage: percentage,
		Unit:       QuotaUnit,
		Exceeded:   total > 0 && remain <= 0,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (c *Client) UserEntUsage(ctx context.Context, accountID string, credential Credential) (general, work entitlementBucket, err error) {
	body, status, err := c.do(ctx, accountID, http.MethodPost, credential.BillingBase()+pathEntUsage, []byte("{}"),
		func(h http.Header) { SetUgHeaders(h, credential) })
	if err != nil {
		return entitlementBucket{}, entitlementBucket{}, err
	}
	if status >= 300 {
		return entitlementBucket{}, entitlementBucket{}, classifiedError(status, body)
	}
	general, work = parseEntitlementBuckets(body)
	if general.total == 0 && general.remain == 0 && general.used == 0 && work.total == 0 && work.remain == 0 && work.used == 0 {
		return entitlementBucket{}, entitlementBucket{}, fmt.Errorf("entitlement parse: empty pack list")
	}
	return general, work, nil
}

// entitlementEndpointWork is the available_endpoint value that marks the
// "Work 专属积分" bucket; everything else is General.
const entitlementEndpointWork = 1

type entitlementBucket struct {
	remain int64
	used   int64
	total  int64
}

// parseEntitlementUsage sums every credit pack (both buckets).
func parseEntitlementUsage(body []byte) (remain, used, total int64) {
	general, work := parseEntitlementBuckets(body)
	return general.remain + work.remain, general.used + work.used, general.total + work.total
}

// parseEntitlementBuckets splits the credit packs into the General bucket
// (available_endpoint 0) and the Work-only bucket (available_endpoint 1). The
// Trae dashboard reports these separately as 通用积分 / Work 专属.
func parseEntitlementBuckets(body []byte) (general, work entitlementBucket) {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return entitlementBucket{}, entitlementBucket{}
	}
	for _, pack := range entitlementPacks(root) {
		limit := int64FromAny(lookupPath(pack, "entitlement_base_info", "quota", "credits_limit"))
		if limit <= 0 {
			limit = int64FromAny(lookupPath(pack, "quota", "credits_limit"))
		}
		if limit <= 0 {
			continue
		}
		consumed := int64FromAny(lookupPath(pack, "usage", "credits_amount"))
		if consumed < 0 {
			consumed = 0
		}
		left := limit - consumed
		if left < 0 {
			left = 0
		}
		bucket := &general
		if isWorkPack(pack) {
			bucket = &work
		}
		bucket.total += limit
		bucket.used += consumed
		bucket.remain += left
	}
	return general, work
}

// isWorkPack reports whether a credit pack belongs to the Work-only bucket.
func isWorkPack(pack any) bool {
	value := lookupPath(pack, "entitlement_base_info", "available_endpoint")
	if value == nil {
		value = lookupPath(pack, "available_endpoint")
	}
	return int64FromAny(value) == entitlementEndpointWork
}

func entitlementPacks(root any) []any {
	switch typed := root.(type) {
	case []any:
		return typed
	case map[string]any:
		for _, key := range []string{"user_entitlement_pack_list", "data", "Result", "result"} {
			if nested, ok := typed[key]; ok {
				if packs := entitlementPacks(nested); len(packs) > 0 {
					return packs
				}
			}
		}
	}
	return nil
}

func lookupPath(value any, keys ...string) any {
	current := value
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		next, ok := object[key]
		if !ok {
			found := false
			for existing, nested := range object {
				if strings.EqualFold(existing, key) {
					next = nested
					found = true
					break
				}
			}
			if !found {
				return nil
			}
		}
		current = next
	}
	return current
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	case string:
		n, _ := json.Number(strings.TrimSpace(typed)).Int64()
		return n
	default:
		return 0
	}
}

func (c *Client) CheckinStatus(ctx context.Context, accountID string, credential Credential) ([]byte, error) {
	body, status, err := c.do(ctx, accountID, http.MethodPost, credential.BillingBase()+pathCheckinStatus, []byte(`{"req_source":1}`),
		func(h http.Header) { SetUgHeaders(h, credential) })
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, checkinHTTPError(status, body)
	}
	return body, nil
}

func (c *Client) CheckinClaim(ctx context.Context, accountID string, credential Credential) ([]byte, error) {
	body, status, err := c.do(ctx, accountID, http.MethodPost, credential.BillingBase()+pathCheckinClaim, []byte(`{"req_source":1}`),
		func(h http.Header) { SetUgHeaders(h, credential) })
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, checkinHTTPError(status, body)
	}
	return body, nil
}

func (c *Client) Adapter() providers.Adapter {
	return providers.Adapter{
		ID:                    "trae",
		Credential:            credentialCodec{},
		Login:                 c,
		Chat:                  c,
		Models:                c,
		Classifier:            classifier{},
		ImportExport:          importer{},
		Prober:                c,
		Checkin:               c,
		CredentialMaintenance: c,
	}
}

// Keepalive checks expiry using the same credential lock/CAS path as chat.
func (c *Client) Keepalive(ctx context.Context, accountID string) error {
	_, err := c.credential(ctx, accountID)
	return err
}

type credentialCodec struct{}

func (credentialCodec) Validate(payload []byte) error { return ValidateCredential(payload) }

type classifier struct{}

func (classifier) Classify(status int, body string) providers.ClassifiedError {
	return Classify(status, body)
}

type importer struct{}

func (importer) ValidateImport(payload []byte) error { return ValidateCredential(payload) }

func (importer) Export(ctx context.Context, accountID string) (map[string]any, error) {
	return nil, providers.ErrUnsupported
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
