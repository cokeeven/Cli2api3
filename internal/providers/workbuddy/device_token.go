// device_token.go X-Device-Token 设备指纹兜底读取与注入。
//
// 移植自 workbuddy2api (internal/upstream/device_token.go)，适配 cli2api 的
// Credential 模型。容器内无桌面端 Turing Shield SDK，无法现取 token，故提供
// 文件兜底路径：宿主把桌面端生成的 device token 落盘，网关定期读取注入。
// 三级优先级：credential.DeviceToken（每号）> 全局 secret > 文件兜底。
// 读失败 / 超 1KB / 空白一律优雅降级（不注入该头），不影响主流程。
package workbuddy

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var errDeviceTokenTooLarge = errors.New("device token file too large")

// deviceTokenFileTTL 文件读取缓存 TTL。桌面端 SDK 自身有缓存，这里再兜一层，
// 避免每次出站请求都 stat+read。
const deviceTokenFileTTL = 5 * time.Minute

// deviceTokenFileMaxLen token 文件最大字节数。token 通常几百字节；超过 1KB
// 视为异常（非 token 内容 / 文件被误用），忽略不注入。
const deviceTokenFileMaxLen = 1024

// deviceTokenFileCache 缓存 device token 文件读取结果。
type deviceTokenFileCache struct {
	mu     sync.Mutex
	path   string
	token  string
	readAt time.Time
}

var dtFileCache = &deviceTokenFileCache{}

// readDeviceTokenFile 读取并缓存 device token 文件；TTL 内复用上次结果。
// 返回空串表示无可用 token（未配置 / 读失败 / 过长 / 空白）。
func readDeviceTokenFile(path string) string {
	if path == "" {
		return ""
	}
	dtFileCache.mu.Lock()
	defer dtFileCache.mu.Unlock()
	if path == dtFileCache.path && time.Since(dtFileCache.readAt) < deviceTokenFileTTL {
		return dtFileCache.token
	}
	dtFileCache.path = path
	tok, err := readTrimmedFile(path, deviceTokenFileMaxLen)
	if err != nil {
		dtFileCache.token = ""
		dtFileCache.readAt = time.Now()
		return ""
	}
	dtFileCache.token = tok
	dtFileCache.readAt = time.Now()
	return tok
}

func readTrimmedFile(path string, maxLen int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > int64(maxLen) {
		return "", errDeviceTokenTooLarge
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// ---------------------------------------------------------------------------
// 设备指纹注入（三级解析：每号 > 全局 > 文件兜底）
// ---------------------------------------------------------------------------

// DeviceTokenConfig 设备指纹的全局兜底配置。两个字段都可空：
//   - GlobalToken：所有未单独配置 token 的账号共用的指纹（env/secret 注入）。
//   - TokenFilePath：宿主落盘的 device token 文件路径（桌面端 SDK 输出），
//     走带 5min 缓存的文件读取，≤1KB，读失败优雅降级。
type DeviceTokenConfig struct {
	GlobalToken   string
	TokenFilePath string
}

// deviceTokenConfig 包级兜底配置，由 ConfigureDeviceToken 设置（进程启动时
// 从 env / secret 读一次）。零值 = 只做"每号"注入，无全局/文件兜底。
var deviceTokenConfig = struct {
	mu     sync.RWMutex
	global string
	file   string
}{}

// ConfigureDeviceToken 设置全局/文件兜底。可在启动时调用一次；并发安全。
func ConfigureDeviceToken(cfg DeviceTokenConfig) {
	deviceTokenConfig.mu.Lock()
	defer deviceTokenConfig.mu.Unlock()
	deviceTokenConfig.global = strings.TrimSpace(cfg.GlobalToken)
	deviceTokenConfig.file = strings.TrimSpace(cfg.TokenFilePath)
}

// resolveDeviceToken 按优先级解析 device token：
//  1. perAccount（credential.DeviceToken，号级隔离，最高优先）
//  2. 全局兜底 token
//  3. 文件兜底（带缓存）
// 全部为空则返回空串（调用方跳过该头）。
func resolveDeviceToken(perAccount string) string {
	if v := strings.TrimSpace(perAccount); v != "" {
		return v
	}
	deviceTokenConfig.mu.RLock()
	global, file := deviceTokenConfig.global, deviceTokenConfig.file
	deviceTokenConfig.mu.RUnlock()
	if global != "" {
		return global
	}
	return readDeviceTokenFile(file)
}

// InjectDeviceToken 把解析出的 device token 写入 X-Device-Token 头。
// 解析为空时不设该头（优雅降级，不影响请求）。
func InjectDeviceToken(header http.Header, credential Credential) {
	if tok := resolveDeviceToken(credential.DeviceToken); tok != "" {
		header.Set("X-Device-Token", tok)
	}
}
