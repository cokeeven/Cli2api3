package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Host              string
	Port              int
	ProxyAPIKey       string
	ProxyURL          string
	MaxRetryAccounts  int
	QoderHome         string
	DataDir           string
	RuntimeDir        string
	WorkerBasePort    int
	NodeBinary        string
	WorkerDaemonPath  string
	QoderCLIPath      string
	QoderCNCLIPath    string
	PlainTemplatePath string
	UpdateSocketPath  string
	UpdateAgentURL    string
	UpdateAgentToken  string
	UpdateGitHubToken string
	// WorkBuddyDeviceToken is the shared Turing Shield device fingerprint
	// (X-Device-Token) for accounts without a per-account token. Optional.
	WorkBuddyDeviceToken string
	// WorkBuddyDeviceTokenFile is a host-side file the desktop SDK writes the
	// device token to; read with a 5-min cache as the lowest-priority fallback.
	WorkBuddyDeviceTokenFile string
}

func Load() (Config, error) {
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
	maxRetryAccounts := 4
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
		Host:              host,
		Port:              port,
		ProxyAPIKey:       "",
		ProxyURL:          strings.TrimSpace(os.Getenv("QODER_PROXY_URL")),
		WorkBuddyDeviceToken:     strings.TrimSpace(os.Getenv("WORKBUDDY_DEVICE_TOKEN")),
		WorkBuddyDeviceTokenFile: strings.TrimSpace(os.Getenv("WORKBUDDY_DEVICE_TOKEN_FILE")),
		MaxRetryAccounts:  maxRetryAccounts,
		QoderHome:         home,
		DataDir:           dataDir,
		RuntimeDir:        runtimeDir,
		WorkerBasePort:    workerBasePort,
		NodeBinary:        firstNonEmpty(os.Getenv("QODER_NODE_BINARY"), "node"),
		WorkerDaemonPath:  firstNonEmpty(os.Getenv("QODER_WORKER_DAEMON"), "worker/src/daemon.mjs"),
		QoderCLIPath:      firstNonEmpty(os.Getenv("QODERCLI_JS"), "/usr/local/lib/node_modules/@qoder-ai/qodercli/bundle/qodercli.js"),
		QoderCNCLIPath:    firstNonEmpty(os.Getenv("QODERCNCLI_JS"), "/usr/local/lib/node_modules/@qodercn-ai/qoderclicn/bundle/qoderclicn.js"),
		PlainTemplatePath: firstNonEmpty(os.Getenv("PLAIN_TEMPLATE_PATH"), "worker/last-plain.sample.json"),
		UpdateSocketPath:  firstNonEmpty(os.Getenv("UPDATE_SOCKET_PATH"), "/run/cli2api-updater/updater.sock"),
		UpdateAgentURL:    strings.TrimSpace(os.Getenv("UPDATE_AGENT_URL")),
		UpdateAgentToken:  strings.TrimSpace(os.Getenv("UPDATE_AGENT_TOKEN")),
		UpdateGitHubToken: strings.TrimSpace(os.Getenv("UPDATE_GITHUB_TOKEN")),
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
