// Package update implements hydra's self-update: checking GitHub Releases
// for a newer version and replacing the running binary in place.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hydra/internal/core"
)

const repo = "ChaudharyRaman/hydra"

func assetBase() string { return fmt.Sprintf("hydra_%s_%s", runtime.GOOS, runtime.GOARCH) }

// LatestTag returns the newest published release tag (e.g. "v0.2.0").
func LatestTag(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.TagName == "" {
		return "", fmt.Errorf("no release tag found")
	}
	return r.TagName, nil
}

// IsNewer reports whether latest is a different (newer) release than the
// running version. A "dev" build is never considered up to date.
func IsNewer(current, latest string) bool {
	current = strings.TrimPrefix(current, "v")
	latest = strings.TrimPrefix(latest, "v")
	if current == "dev" || current == "" {
		return true
	}
	return latest != "" && latest != current
}

// Apply downloads the latest release for this OS/arch and atomically replaces
// the running executable.
func Apply(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("self-update isn't supported on Windows yet — re-run the installer or grab the binary from Releases")
	}
	url := fmt.Sprintf("https://github.com/%s/releases/latest/download/%s.tar.gz", repo, assetBase())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}

	// Stage the new binary in the same directory so the final rename is
	// atomic (same filesystem) and safe while the old binary is running.
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".hydra-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		tmp.Close()
		return err
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			tmp.Close()
			return err
		}
		if filepath.Base(hdr.Name) == "hydra" {
			if _, err := io.Copy(tmp, tr); err != nil {
				tmp.Close()
				return err
			}
			found = true
			break
		}
	}
	tmp.Close()
	if !found {
		return fmt.Errorf("release archive did not contain a hydra binary")
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

// ---- throttled startup check ----

type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func cachePath() string { return filepath.Join(core.HydraDir(), "update-check.json") }

// LatestCachedOrFetch returns the latest tag, hitting the network at most
// once every 24h. Used by the startup nudge so launches stay fast and quiet.
func LatestCachedOrFetch(ctx context.Context) (string, error) {
	if data, err := os.ReadFile(cachePath()); err == nil {
		var c cache
		if json.Unmarshal(data, &c) == nil && c.Latest != "" && time.Since(c.CheckedAt) < 24*time.Hour {
			return c.Latest, nil
		}
	}
	tag, err := LatestTag(ctx)
	if err != nil {
		return "", err
	}
	if data, err := json.Marshal(cache{CheckedAt: time.Now(), Latest: tag}); err == nil {
		os.MkdirAll(core.HydraDir(), 0o755)
		os.WriteFile(cachePath(), data, 0o644)
	}
	return tag, nil
}
