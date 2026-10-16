package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/caigee-cmd/cli2api/internal/updatenet"
)

const imageManifestName = "cli2api-image-digest.json"

type imageManifest struct {
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
}

var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (e *Executor) NetworkStatus() updatenet.Status {
	config, err := updatenet.Load(e.config.NetworkFile)
	status := config.Status()
	if err != nil {
		status.Error = err.Error()
	}
	e.networkMu.Lock()
	status.ImageSource, status.DownloadSource = e.network.ImageSource, e.network.DownloadSource
	status.ImageFallback, status.DownloadFallback = e.network.ImageFallback, e.network.DownloadFallback
	status.Fallback = status.ImageFallback || status.DownloadFallback
	e.networkMu.Unlock()
	return status
}

func (e *Executor) recordNetworkSource(channel, source string, fallback bool) {
	e.networkMu.Lock()
	defer e.networkMu.Unlock()
	if channel == "image" {
		e.network.ImageSource = source
		e.network.ImageFallback = fallback
	} else {
		e.network.DownloadSource = source
		e.network.DownloadFallback = fallback
	}
}

func (e *Executor) pullTargetImage(ctx context.Context, version string) error {
	config, err := updatenet.Load(e.config.NetworkFile)
	if err != nil {
		return err
	}
	target := e.config.ImageRepository + ":" + version
	pullCtx, cancel := context.WithTimeout(ctx, time.Duration(config.PullTimeout)*time.Second)
	_, err = e.runner.Run(pullCtx, "docker", "pull", target)
	cancel()
	if err == nil {
		e.recordNetworkSource("image", e.config.ImageRepository, false)
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(config.ImageMirrors) == 0 {
		return fmt.Errorf("primary image pull failed; no image mirrors configured (Docker daemon proxy is configured separately)")
	}
	// Resolve the immutable multi-platform digest from release metadata before
	// asking any alternate registry for executable content. Never trust a tag
	// with the same spelling on an unrelated registry.
	body, err := e.downloadReleaseFile(ctx, version, imageManifestName)
	if err != nil {
		return fmt.Errorf("cannot verify mirror image digest: %w", err)
	}
	var manifest imageManifest
	if err := json.Unmarshal(body, &manifest); err != nil || manifest.Version != version || manifest.Repository != e.config.ImageRepository || !imageDigestPattern.MatchString(manifest.Digest) {
		return fmt.Errorf("invalid image digest manifest for requested release/repository")
	}
	for _, mirror := range config.ImageMirrors {
		if err := ctx.Err(); err != nil {
			return err
		}
		ref := mirror + "@" + manifest.Digest
		pullCtx, cancel := context.WithTimeout(ctx, time.Duration(config.PullTimeout)*time.Second)
		_, pullErr := e.runner.Run(pullCtx, "docker", "pull", ref)
		cancel()
		if pullErr != nil {
			continue
		}
		// Docker verifies the requested manifest/layers by digest. Retag only a
		// successful pull; deployment and rollback keep the canonical repository.
		if _, err := e.runner.Run(ctx, "docker", "image", "tag", ref, target); err != nil {
			return fmt.Errorf("cannot tag verified mirror image")
		}
		e.recordNetworkSource("image", mirror, true)
		return nil
	}
	return fmt.Errorf("primary image and all %d mirror pulls failed; current service retained: %s", len(config.ImageMirrors), strings.Join(config.ImageMirrors, ", "))
}
