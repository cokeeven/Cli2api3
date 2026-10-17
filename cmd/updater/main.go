package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/caigee-cmd/cli2api/internal/updater"
)

func main() {
	var validateNative bool
	flag.BoolVar(&validateNative, "validate-native-config", false, "validate native onboarding paths and identity without starting a daemon")
	var runGateway, superviseNative string
	flag.StringVar(&runGateway, "run-native-gateway", "", "run gateway using preserved native launch config")
	flag.StringVar(&superviseNative, "supervise-native", "", "independently supervise a native updater")
	var packagePath, checksumsPath, extractPath, targetVersion, databasePath, backupDir string
	flag.StringVar(&packagePath, "unpack-native-package", "", "verify and unpack a Windows application archive, then exit")
	flag.StringVar(&checksumsPath, "package-checksums", "", "application checksum file")
	flag.StringVar(&extractPath, "extract-to", "", "new staging directory")
	flag.StringVar(&targetVersion, "target-version", "", "expected stable application version")
	flag.StringVar(&databasePath, "snapshot-database", "", "snapshot an existing SQLite database without migrations, then exit")
	flag.StringVar(&backupDir, "backup-directory", "", "snapshot destination")
	var backend, nativeConfigPath string
	flag.StringVar(&backend, "backend", "docker", "deployment backend: docker or windows-native")
	flag.StringVar(&nativeConfigPath, "native-config", "", "absolute Windows native updater JSON configuration")
	var socketPath string
	var listenAddress string
	var authToken string
	var authTokenFile string
	var statusFile string
	var composeFile string
	var envFile string
	var serviceName string
	var containerName string
	var imageRepository string
	var networkFile string
	var healthURL string
	var healthTimeout time.Duration
	flag.StringVar(&socketPath, "socket", "/run/cli2api-updater/updater.sock", "Unix socket path")
	flag.StringVar(&listenAddress, "listen", "", "loopback TCP listen address")
	flag.StringVar(&authToken, "auth-token", "", "Bearer token required for TCP mode")
	flag.StringVar(&authTokenFile, "auth-token-file", "", "file containing the TCP Bearer token")
	flag.StringVar(&statusFile, "status-file", "/var/lib/cli2api-updater/status.json", "persistent updater status")
	flag.StringVar(&composeFile, "compose-file", "", "absolute docker-compose.yml path")
	flag.StringVar(&envFile, "env-file", "", "absolute Compose env file path")
	flag.StringVar(&serviceName, "service", "qoder-api-proxy", "Compose service name")
	flag.StringVar(&containerName, "container", "qoder-api-proxy", "container name")
	flag.StringVar(&imageRepository, "image-repository", "ghcr.io/cokeeven/ali2api3", "allowed image repository")
	flag.StringVar(&networkFile, "network-config", os.Getenv("UPDATE_NETWORK_CONFIG"), "update mirrors/proxy JSON file (reloaded for each operation)")
	flag.StringVar(&healthURL, "health-url", "http://127.0.0.1:3010/health", "service health URL")
	flag.DurationVar(&healthTimeout, "health-timeout", 120*time.Second, "versioned health-check timeout")
	flag.Parse()
	if runGateway != "" {
		if err := updater.RunNativeGateway(runGateway); err != nil {
			log.Fatal(err)
		}
		return
	}
	if superviseNative != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := updater.RunNativeSupervisor(ctx, superviseNative); err != nil {
			log.Fatal(err)
		}
		return
	}
	if packagePath != "" {
		m, err := updater.UnpackNativePackage(packagePath, checksumsPath, targetVersion, extractPath)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Verified %s %s %s\n", m.Version, m.Arch, m.Commit)
		return
	}
	if databasePath != "" {
		p, err := updater.SnapshotNativeDatabase(context.Background(), databasePath, backupDir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(p)
		return
	}
	if strings.TrimSpace(authToken) == "" && strings.TrimSpace(authTokenFile) != "" {
		data, err := os.ReadFile(authTokenFile)
		if err != nil {
			log.Fatal(err)
		}
		authToken = strings.TrimSpace(string(data))
	}

	hostBinary, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	serviceConfig := updater.Config{SocketPath: socketPath, ListenAddress: listenAddress, AuthToken: authToken, StatusFile: statusFile}
	var service *updater.Service
	if backend == updater.NativeBackend {
		raw, err := os.ReadFile(nativeConfigPath)
		if err != nil {
			log.Fatal(err)
		}
		var cfg updater.NativeConfig
		if err = json.Unmarshal(raw, &cfg); err != nil {
			log.Fatal(err)
		}
		if !validateNative {
			if err := updater.ValidateNativeListen(listenAddress); err != nil {
				log.Fatal(err)
			}
			if !filepath.IsAbs(statusFile) || !strings.EqualFold(filepath.Clean(filepath.Dir(statusFile)), filepath.Clean(cfg.StateDir)) {
				log.Fatal("native status-file must be an absolute path inside configured state_dir")
			}
			serviceConfig.RecoverAfterListen = true
		}
		executor, err := updater.NewNativeExecutor(cfg, authToken, networkFile)
		if err != nil {
			log.Fatal(err)
		}
		if validateNative {
			executor.Close()
			fmt.Println("Native configuration verified")
			return
		}
		service = updater.NewService(serviceConfig, executor)
	} else if backend == "docker" {
		executor := updater.NewExecutor(updater.ExecutorConfig{
			ComposeFile: composeFile, EnvFile: envFile, ServiceName: serviceName,
			ContainerName: containerName, ImageRepository: imageRepository, HealthURL: healthURL,
			HealthTimeout: healthTimeout, HostBinaryPath: hostBinary,
			GitHubToken: strings.TrimSpace(os.Getenv("UPDATE_GITHUB_TOKEN")),
			NetworkFile: networkFile,
		})
		service = updater.NewService(serviceConfig, executor)
	} else {
		log.Fatal("unknown updater backend")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := service.Serve(ctx); err != nil {
		log.Fatal(err)
	}
}
