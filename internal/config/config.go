package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	SourceFingerprintEnabled    bool
	TrustedProxyCIDRs           []string
	ChatHeadersTimeout          time.Duration
	ChatFirstEventTimeout       time.Duration
	ChatIdleTimeout             time.Duration
	ChatTotalTimeout            time.Duration
	ChatCapacityQueueMaxWaiters int
	ChatCapacityQueueWait       time.Duration
	Host                        string
	Port                        int
	ProxyAPIKey                 string
	ProxyURL                    string
	MaxRetryAccounts            int
	QoderHome                   string
	DataDir                     string
	RuntimeDir                  string
	WorkerBasePort              int
	NodeBinary                  string
	WorkerDaemonPath            string
	QoderCLIPath                string
	QoderCNCLIPath              string
	PlainTemplatePath           string
	UpdateSocketPath            string
	UpdateBackend               string
	UpdateAgentURL              string
	UpdateAgentToken            string
	UpdateGitHubToken           string
	UpdateNetworkFile           string
	// WorkBuddyDeviceToken is the shared Turing Shield device fingerprint
	// (X-Device-Token) for accounts without a per-account token. Optional.
	WorkBuddyDeviceToken string
	// WorkBuddyDeviceTokenFile is a host-side file the desktop SDK writes the
	// device token to; read with a 5-min cache as the lowest-priority fallback.
	WorkBuddyDeviceTokenFile string
}

func Load() (Config, error) {
	sourceEnabled := false
	if raw := strings.TrimSpace(os.Getenv("CLI2API_SOURCE_FINGERPRINT_ENABLED")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid CLI2API_SOURCE_FINGERPRINT_ENABLED")
		}
		sourceEnabled = value
	}
	var trustedProxies []string
	if raw := strings.TrimSpace(os.Getenv("CLI2API_TRUSTED_PROXY_CIDRS")); raw != "" {
		trustedProxies = strings.Split(raw, ",")
	}
	deadlines := make([]time.Duration, 4)
	for i, item := range []struct {
		name     string
		fallback int
	}{
		{"CLI2API_CHAT_HEADERS_TIMEOUT_SECONDS", 45},
		{"CLI2API_CHAT_FIRST_EVENT_TIMEOUT_SECONDS", 120},
		{"CLI2API_CHAT_IDLE_TIMEOUT_SECONDS", 90},
		{"CLI2API_CHAT_TOTAL_TIMEOUT_SECONDS", 300},
	} {
		seconds := item.fallback
		if raw := strings.TrimSpace(os.Getenv(item.name)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 3600 {
				return Config{}, fmt.Errorf("%s must be an integer between 1 and 3600", item.name)
			}
			seconds = value
		}
		deadlines[i] = time.Duration(seconds) * time.Second
	}
	capacityQueueMaxWaiters := 64
	if raw := strings.TrimSpace(os.Getenv("CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 256 {
			return Config{}, fmt.Errorf("CLI2API_CHAT_CAPACITY_QUEUE_MAX_WAITERS must be an integer between 0 and 256")
		}
		capacityQueueMaxWaiters = value
	}
	capacityQueueWaitSeconds := 5
	if raw := strings.TrimSpace(os.Getenv("CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 60 {
			return Config{}, fmt.Errorf("CLI2API_CHAT_CAPACITY_QUEUE_WAIT_SECONDS must be an integer between 0 and 60")
		}
		capacityQueueWaitSeconds = value
	}
	port := 3010
	if v := strings.TrimSpace(os.Getenv("PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	home := strings.TrimSpace(os.Getenv("QODER_HOME"))
	if home == "" {
		home = "/root/.qoder"
	}
	host := strings.TrimSpace(os.Getenv("HOST"))
	if host == "" {
		host = "127.0.0.1"
	}
	workerBasePort := 32100
	if v := strings.TrimSpace(os.Getenv("QODER_WORKER_BASE_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerBasePort = n
		}
	}
	maxRetryAccounts := 64
	if v := strings.TrimSpace(os.Getenv("QODER_MAX_RETRY_ACCOUNTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxRetryAccounts = n
		}
	}
	if maxRetryAccounts > 64 {
		maxRetryAccounts = 64
	}
	dataDir := strings.TrimSpace(os.Getenv("QODER_DATA_DIR"))
	if dataDir == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			dataDir = userHome + "/.qoder-api-proxy"
		} else {
			dataDir = "data"
		}
	}
	runtimeDir := strings.TrimSpace(os.Getenv("QODER_RUNTIME_DIR"))
	if runtimeDir == "" {
		runtimeDir = "/tmp/cli2api-runtime"
	}
	return Config{
		SourceFingerprintEnabled:    sourceEnabled,
		TrustedProxyCIDRs:           trustedProxies,
		ChatHeadersTimeout:          deadlines[0],
		ChatFirstEventTimeout:       deadlines[1],
		ChatIdleTimeout:             deadlines[2],
		ChatTotalTimeout:            deadlines[3],
		ChatCapacityQueueMaxWaiters: capacityQueueMaxWaiters,
		ChatCapacityQueueWait:       time.Duration(capacityQueueWaitSeconds) * time.Second,
		Host:                        host,
		Port:                        port,
		ProxyAPIKey:                 "",
		ProxyURL:                    strings.TrimSpace(os.Getenv("QODER_PROXY_URL")),
		WorkBuddyDeviceToken:        strings.TrimSpace(os.Getenv("WORKBUDDY_DEVICE_TOKEN")),
		WorkBuddyDeviceTokenFile:    strings.TrimSpace(os.Getenv("WORKBUDDY_DEVICE_TOKEN_FILE")),
		MaxRetryAccounts:            maxRetryAccounts,
		QoderHome:                   home,
		DataDir:                     dataDir,
		RuntimeDir:                  runtimeDir,
		WorkerBasePort:              workerBasePort,
		NodeBinary:                  firstNonEmpty(os.Getenv("QODER_NODE_BINARY"), "node"),
		WorkerDaemonPath:            firstNonEmpty(os.Getenv("QODER_WORKER_DAEMON"), "worker/src/daemon.mjs"),
		QoderCLIPath:                firstNonEmpty(os.Getenv("QODERCLI_JS"), "/usr/local/lib/node_modules/@qoder-ai/qodercli/bundle/qodercli.js"),
		QoderCNCLIPath:              firstNonEmpty(os.Getenv("QODERCNCLI_JS"), "/usr/local/lib/node_modules/@qodercn-ai/qoderclicn/bundle/qoderclicn.js"),
		PlainTemplatePath:           firstNonEmpty(os.Getenv("PLAIN_TEMPLATE_PATH"), "worker/last-plain.sample.json"),
		UpdateSocketPath:            firstNonEmpty(os.Getenv("UPDATE_SOCKET_PATH"), "/run/cli2api-updater/updater.sock"),
		UpdateBackend:               firstNonEmpty(os.Getenv("UPDATE_BACKEND"), "docker"),
		UpdateAgentURL:              strings.TrimSpace(os.Getenv("UPDATE_AGENT_URL")),
		UpdateAgentToken:            strings.TrimSpace(os.Getenv("UPDATE_AGENT_TOKEN")),
		UpdateGitHubToken:           strings.TrimSpace(os.Getenv("UPDATE_GITHUB_TOKEN")),
		UpdateNetworkFile:           firstNonEmpty(os.Getenv("UPDATE_NETWORK_CONFIG"), "/run/cli2api-updater/network.json"),
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
