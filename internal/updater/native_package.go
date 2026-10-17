package updater

import (
	"archive/zip"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const NativeBackend = "windows-native"
const NativeAsset = "cli2api-app_windows_amd64.zip"
const NativeChecksums = "cli2api-app_checksums.txt"
const maxNativeBytes = 768 << 20
const maxNativeExpanded = 2 << 30

type NativeFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type NativeManifest struct {
	Format        int               `json:"format"`
	Backend       string            `json:"backend"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Version       string            `json:"version"`
	Commit        string            `json:"commit"`
	AgentProtocol int               `json:"agent_protocol"`
	Components    map[string]string `json:"components"`
	Files         []NativeFile      `json:"files"`
}

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var nativeRoots = []string{"qoder-api-proxy.exe", "components", "native-manifest.json"}

func safeNativeName(name string) bool {
	if name == "" || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00<>\"|?*") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == "." || p == ".." || strings.TrimRight(p, " .") != p {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if stem == "CON" || stem == "NUL" || stem == "PRN" || stem == "AUX" || stem == "CONIN$" || stem == "CONOUT$" || stem == "CLOCK$" || regexp.MustCompile(`^(COM|LPT)[0-9]$`).MatchString(stem) {
			return false
		}
	}
	return name == "native-manifest.json" || name == "qoder-api-proxy.exe" || name == "components" || strings.HasPrefix(name, "components/")
}

func noLinks(root string) error {
	current, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for {
		info, e := os.Lstat(current)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked update path is forbidden: %s", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func fileDigest(p string) (string, int64, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}

// Extract only to a fresh staging directory. Never extract into a live program/data tree.
func extractNative(archive, destination, version string) (NativeManifest, error) {
	var m NativeManifest
	if err := noLinks(destination); err != nil {
		return m, err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return m, err
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		return m, fmt.Errorf("invalid application zip: %w", err)
	}
	defer z.Close()
	if len(z.File) > 30000 {
		return m, fmt.Errorf("too many application files")
	}
	seen := map[string]bool{}
	var expanded uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !safeNativeName(name) || f.Mode()&os.ModeSymlink != 0 || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return m, fmt.Errorf("unsafe package entry %q", f.Name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return m, fmt.Errorf("duplicate package path %q", name)
		}
		seen[key] = true
		if f.UncompressedSize64 > maxNativeExpanded {
			return m, fmt.Errorf("application file exceeds expanded size limit")
		}
		expanded += f.UncompressedSize64
		if expanded > maxNativeExpanded {
			return m, fmt.Errorf("expanded application exceeds size limit")
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := noLinks(target); err != nil {
			return m, err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return m, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return m, err
		}
		in, e := f.Open()
		if e != nil {
			return m, e
		}
		out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			in.Close()
			return m, e
		}
		n, e := io.Copy(out, io.LimitReader(in, int64(f.UncompressedSize64)+1))
		in.Close()
		syncErr := out.Sync()
		closeErr := out.Close()
		if e != nil || syncErr != nil || closeErr != nil || n != int64(f.UncompressedSize64) {
			return m, fmt.Errorf("incomplete package file %q", name)
		}
	}
	return verifyNativeTree(destination, version)
}

func verifyNativeTree(root, version string) (NativeManifest, error) {
	var m NativeManifest
	raw, err := os.ReadFile(filepath.Join(root, "native-manifest.json"))
	if err != nil {
		return m, err
	}
	if len(raw) > 8<<20 {
		return m, fmt.Errorf("manifest too large")
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	if m.Format != 1 || m.Backend != NativeBackend || m.OS != "windows" || m.Arch != "amd64" || m.Version != version || !commitPattern.MatchString(m.Commit) || m.AgentProtocol != 2 {
		return m, fmt.Errorf("application manifest version, architecture or protocol mismatch")
	}
	required := map[string]bool{"qoder-api-proxy.exe": false, "components/node/node.exe": false, "components/worker/src/daemon.mjs": false, "components/worker/src/compat.mjs": false, "components/worker/node_modules/undici/package.json": false, "components/cli/node_modules/@qoder-ai/qodercli/bundle/qodercli.js": false, "components/cli/node_modules/@qodercn-ai/qoderclicn/bundle/qoderclicn.js": false, "components/updater.exe": false}
	seen := map[string]bool{"native-manifest.json": true}
	for _, f := range m.Files {
		key := strings.ToLower(f.Path)
		if !safeNativeName(f.Path) || seen[key] || f.Size < 0 || len(f.SHA256) != 64 {
			return m, fmt.Errorf("invalid manifest entry %q", f.Path)
		}
		seen[key] = true
		target := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := noLinks(target); err != nil {
			return m, err
		}
		sum, n, e := fileDigest(target)
		if e != nil || n != f.Size || sum != f.SHA256 {
			return m, fmt.Errorf("application checksum mismatch: %s", f.Path)
		}
		if _, ok := required[f.Path]; ok {
			required[f.Path] = true
		}
	}
	for name, ok := range required {
		if !ok {
			return m, fmt.Errorf("required component missing: %s", name)
		}
	}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked component")
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		if !seen[strings.ToLower(filepath.ToSlash(rel))] {
			return fmt.Errorf("unlisted component %s", rel)
		}
		return nil
	})
	if err != nil {
		return m, err
	}
	for _, p := range []string{"qoder-api-proxy.exe", "components/node/node.exe", "components/updater.exe"} {
		f, e := pe.Open(filepath.Join(root, filepath.FromSlash(p)))
		if e != nil {
			return m, fmt.Errorf("invalid Windows executable %s", p)
		}
		machine := f.Machine
		f.Close()
		if machine != pe.IMAGE_FILE_MACHINE_AMD64 {
			return m, fmt.Errorf("wrong executable architecture: %s", p)
		}
	}
	if m.Components["qodercli"] != "1.1.32" || m.Components["qoderclicn"] != "1.1.32" || m.Components["node"] == "" {
		return m, fmt.Errorf("runtime compatibility versions missing")
	}
	return m, nil
}
