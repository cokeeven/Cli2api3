package workbuddy

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceTokenInjectPriority(t *testing.T) {
	t.Cleanup(func() { ConfigureDeviceToken(DeviceTokenConfig{}) })
	// 1) 号级 token 最高优先（盖过全局/文件）
	dir := t.TempDir()
	fp := filepath.Join(dir, "dt.txt")
	_ = os.WriteFile(fp, []byte("dt-file"), 0o644)
	ConfigureDeviceToken(DeviceTokenConfig{GlobalToken: "dt-global", TokenFilePath: fp})
	h := http.Header{}
	InjectDeviceToken(h, Credential{DeviceToken: "dt-account"})
	if got := h.Get("X-Device-Token"); got != "dt-account" {
		t.Fatalf("per-account priority: got %q", got)
	}

	// 2) 无号级 → 全局兜底
	h2 := http.Header{}
	InjectDeviceToken(h2, Credential{})
	if got := h2.Get("X-Device-Token"); got != "dt-global" {
		t.Fatalf("global fallback: got %q", got)
	}

	// 3) 无号级无全局 → 文件兜底（需清缓存，先重置 path）
	ConfigureDeviceToken(DeviceTokenConfig{GlobalToken: "", TokenFilePath: fp})
	dtFileCache.mu.Lock()
	dtFileCache.path = "" // 强制重读
	dtFileCache.mu.Unlock()
	h3 := http.Header{}
	InjectDeviceToken(h3, Credential{})
	if got := h3.Get("X-Device-Token"); got != "dt-file" {
		t.Fatalf("file fallback: got %q", got)
	}

	// 4) 全空 → 不设头（优雅降级）
	ConfigureDeviceToken(DeviceTokenConfig{})
	h4 := http.Header{}
	InjectDeviceToken(h4, Credential{})
	if _, present := h4["X-Device-Token"]; present {
		t.Fatalf("empty should skip header, got %q", h4.Get("X-Device-Token"))
	}
	// 清理，避免影响其他测试
	ConfigureDeviceToken(DeviceTokenConfig{})
}

func TestSetBillingHeadersInjectsDeviceToken(t *testing.T) {
	t.Cleanup(func() { ConfigureDeviceToken(DeviceTokenConfig{}) })
	ConfigureDeviceToken(DeviceTokenConfig{})
	h := http.Header{}
	SetBillingHeaders(h, Credential{AccessToken: "tok", UID: "u1", DeviceToken: "dt-billing"})
	if got := h.Get("X-Device-Token"); got != "dt-billing" {
		t.Fatalf("SetBillingHeaders inject: got %q", got)
	}
}
