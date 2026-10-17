package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type NativeLaunchConfig struct {
	ProgramDir       string            `json:"program_dir"`
	DataDir          string            `json:"data_dir"`
	WorkingDirectory string            `json:"working_directory"`
	Arguments        []string          `json:"arguments"`
	EnvFile          string            `json:"env_file"`
	RuntimeDir       string            `json:"runtime_dir"`
	Environment      map[string]string `json:"environment"`
	AgentURL         string            `json:"agent_url"`
	TokenFile        string            `json:"token_file"`
	InstanceID       string            `json:"instance_id"`
	NetworkFile      string            `json:"network_file"`
	LogFile          string            `json:"log_file"`
}
type NativeSupervisorConfig struct {
	ProgramDir   string `json:"program_dir"`
	UpdaterDir   string `json:"updater_dir"`
	NativeConfig string `json:"native_config"`
	InstanceID   string `json:"instance_id"`
	Listen       string `json:"listen"`
	TokenFile    string `json:"token_file"`
	StatusFile   string `json:"status_file"`
	NetworkFile  string `json:"network_file"`
	SeedBinary   string `json:"seed_binary"`
	LogFile      string `json:"log_file"`
}

func readNativeConfig(p string, v any) error {
	if err := noLinks(p); err != nil {
		return err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func nativeLog(p string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

func validateNativeLaunch(c NativeConfig, token string) error {
	var launch NativeLaunchConfig
	if err := readNativeConfig(c.LaunchConfig, &launch); err != nil {
		return fmt.Errorf("read native launch configuration: %w", err)
	}
	if !strings.EqualFold(filepath.Clean(launch.ProgramDir), c.ProgramDir) || !strings.EqualFold(filepath.Clean(launch.DataDir), c.DataDir) || launch.InstanceID != c.InstanceID || !filepath.IsAbs(launch.WorkingDirectory) {
		return fmt.Errorf("gateway launch paths/identity do not match native updater")
	}
	credential, err := os.ReadFile(launch.TokenFile)
	if err != nil || strings.TrimSpace(string(credential)) != token {
		return fmt.Errorf("gateway and updater must use the same protected token file")
	}
	values := map[string]string{}
	if launch.EnvFile != "" {
		if !filepath.IsAbs(launch.EnvFile) {
			return fmt.Errorf("gateway environment file must be absolute")
		}
		parsed, e := godotenv.Read(launch.EnvFile)
		if e != nil {
			return fmt.Errorf("cannot read gateway environment file")
		}
		for k, v := range parsed {
			values[strings.ToUpper(k)] = v
		}
	}
	for k, v := range launch.Environment {
		values[strings.ToUpper(k)] = v
	}
	protected := []string{launch.WorkingDirectory, launch.EnvFile, launch.RuntimeDir, values["QODER_HOME"], values["QODER_RUNTIME_DIR"], values["WORKBUDDY_DEVICE_TOKEN_FILE"], launch.LogFile}
	for _, p := range protected {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(launch.WorkingDirectory, p)
		}
		for _, owned := range []string{"components", ".cli2api-update", "qoder-api-proxy.exe", "native-manifest.json"} {
			if pathWithin(p, filepath.Join(c.ProgramDir, owned)) {
				return fmt.Errorf("mutable config/runtime/HOME must be outside managed program components")
			}
		}
	}
	return nil
}

// RunNativeGateway is a stable task action. Only controlled runtime paths are
// changed; original environment, arguments and working directory are retained.
func RunNativeGateway(configPath string) error {
	var c NativeLaunchConfig
	if err := readNativeConfig(configPath, &c); err != nil {
		return err
	}
	values := map[string]string{}
	for _, v := range os.Environ() {
		if i := strings.Index(v, "="); i > 0 {
			values[strings.ToUpper(v[:i])] = v[i+1:]
		}
	}
	if c.EnvFile != "" {
		v, err := godotenv.Read(c.EnvFile)
		if err != nil {
			return fmt.Errorf("cannot read configured gateway environment file")
		}
		for k, s := range v {
			values[strings.ToUpper(k)] = s
		}
	}
	for k, v := range c.Environment {
		values[strings.ToUpper(k)] = v
	}
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return fmt.Errorf("cannot read local updater token")
	}
	if len(strings.TrimSpace(string(token))) < 32 {
		return fmt.Errorf("local updater token is too short")
	}
	managed := map[string]string{"CLI2API_ENV_FILE": c.EnvFile, "QODER_DATA_DIR": c.DataDir, "UPDATE_BACKEND": NativeBackend, "UPDATE_AGENT_URL": c.AgentURL, "UPDATE_AGENT_TOKEN": strings.TrimSpace(string(token)), "UPDATE_INSTANCE_ID": c.InstanceID, "UPDATE_NETWORK_CONFIG": c.NetworkFile, "QODER_NODE_BINARY": filepath.Join(c.ProgramDir, "components", "node", "node.exe"), "QODER_WORKER_DAEMON": filepath.Join(c.ProgramDir, "components", "worker", "src", "daemon.mjs"), "QODERCLI_JS": filepath.Join(c.ProgramDir, "components", "cli", "node_modules", "@qoder-ai", "qodercli", "bundle", "qodercli.js"), "QODERCNCLI_JS": filepath.Join(c.ProgramDir, "components", "cli", "node_modules", "@qodercn-ai", "qoderclicn", "bundle", "qoderclicn.js"), "PLAIN_TEMPLATE_PATH": filepath.Join(c.ProgramDir, "components", "worker", "last-plain.sample.json")}
	if c.RuntimeDir != "" {
		managed["QODER_RUNTIME_DIR"] = c.RuntimeDir
	}
	for k, v := range managed {
		values[k] = v
	}
	output, err := nativeLog(c.LogFile + ".out.log")
	if err != nil {
		return err
	}
	defer output.Close()
	errors, err := nativeLog(c.LogFile + ".err.log")
	if err != nil {
		return err
	}
	defer errors.Close()
	cmd := exec.Command(filepath.Join(c.ProgramDir, "qoder-api-proxy.exe"), c.Arguments...)
	cmd.Dir = c.WorkingDirectory
	cmd.Stdout = output
	cmd.Stderr = errors
	cmd.Env = []string{}
	for k, v := range values {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	hideNativeCommand(cmd)
	return cmd.Run()
}

// RunNativeSupervisor is itself an immutable seed executable. New host binaries
// run from hash-addressed paths, so the running executable is never overwritten.
func RunNativeSupervisor(ctx context.Context, configPath string) error {
	var c NativeSupervisorConfig
	if err := readNativeConfig(configPath, &c); err != nil {
		return err
	}
	release, err := acquireNativePathLock(filepath.Join(c.UpdaterDir, "supervisor.lock"))
	if err != nil {
		return fmt.Errorf("native supervisor already owns this installation: %w", err)
	}
	defer release()
	output, err := nativeLog(c.LogFile + ".out.log")
	if err != nil {
		return err
	}
	defer output.Close()
	errors, err := nativeLog(c.LogFile + ".err.log")
	if err != nil {
		return err
	}
	defer errors.Close()
	lastBinary := c.SeedBinary
	for ctx.Err() == nil {
		binary := lastBinary
		interrupted := false
		var transaction nativeJournal
		if readNativeConfig(filepath.Join(c.UpdaterDir, "transaction.json"), &transaction) == nil {
			interrupted = transaction.Phase != "succeeded" && transaction.Phase != "rolled_back" && transaction.Phase != "aborted"
		}
		raw, err := os.ReadFile(filepath.Join(c.ProgramDir, "native-manifest.json"))
		if err == nil && !interrupted {
			var m NativeManifest
			if err = json.Unmarshal(raw, &m); err != nil {
				return err
			}
			var expected string
			for _, f := range m.Files {
				if f.Path == "components/updater.exe" {
					expected = f.SHA256
				}
			}
			candidate := filepath.Join(c.ProgramDir, "components", "updater.exe")
			sum, _, err := fileDigest(candidate)
			if err != nil || len(expected) != 64 || sum != expected {
				return fmt.Errorf("managed host updater component checksum mismatch")
			}
			binary = filepath.Join(c.UpdaterDir, "bin", expected, "cli2api-updater.exe")
			if _, err = os.Stat(binary); os.IsNotExist(err) {
				if err = os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
					return err
				}
				src, err := os.Open(candidate)
				if err != nil {
					return err
				}
				dst, err := os.CreateTemp(filepath.Dir(binary), ".host-*")
				if err != nil {
					src.Close()
					return err
				}
				_, err = io.Copy(dst, src)
				src.Close()
				syncErr := dst.Sync()
				dst.Close()
				if err != nil || syncErr != nil {
					return fmt.Errorf("cannot install host updater")
				}
				if err = os.Rename(dst.Name(), binary); err != nil {
					return fmt.Errorf("publish host updater: %w", err)
				}
			}
			sum, _, err = fileDigest(binary)
			if err != nil || sum != expected {
				return fmt.Errorf("installed host updater checksum mismatch")
			}
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		lastBinary = binary
		cmd := exec.CommandContext(ctx, binary, "--backend", NativeBackend, "--native-config", c.NativeConfig, "--listen", c.Listen, "--auth-token-file", c.TokenFile, "--status-file", c.StatusFile, "--network-config", c.NetworkFile)
		cmd.Dir = c.UpdaterDir
		cmd.Stdout = output
		cmd.Stderr = errors
		hideNativeCommand(cmd)
		_ = cmd.Run() // Child failures are logged without printing any launch environment/token.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}
