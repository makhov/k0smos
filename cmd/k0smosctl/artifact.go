package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	releaseRepository  = "makhov/k0smos"
	githubAPI          = "https://api.github.com"
	releaseHTTPTimeout = 30 * time.Minute
	releaseAPITimeout  = 30 * time.Second
)

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	APIURL string `json:"url"`
}

type githubRelease struct {
	Tag    string         `json:"tag_name"`
	Assets []releaseAsset `json:"assets"`
}

type artifactResolver struct {
	Client   *http.Client
	API      string
	Repo     string
	CacheDir string
	Release  string
	Arch     string
	Token    string
	Progress io.Writer
}

func resolveReleaseArtifact(ctx context.Context, arch, release, cacheDir string, progress io.Writer) (string, error) {
	dir, err := artifactCacheDir(cacheDir)
	if err != nil {
		return "", err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	return (artifactResolver{
		Client: &http.Client{Timeout: releaseHTTPTimeout}, API: githubAPI, Repo: releaseRepository,
		CacheDir: dir, Release: release, Arch: arch,
		Token: token, Progress: progress,
	}).Resolve(ctx)
}

func artifactCacheDir(requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if dir := os.Getenv("K0SMOS_CACHE_DIR"); dir != "" {
		return filepath.Join(dir, "images"), nil
	}
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "k0smos", "images"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "k0smos", "images"), nil
}

func (r artifactResolver) Resolve(ctx context.Context) (string, error) {
	if r.Client == nil {
		r.Client = http.DefaultClient
	}
	if r.API == "" {
		r.API = githubAPI
	}
	if r.Repo == "" {
		r.Repo = releaseRepository
	}
	if r.Release == "" {
		r.Release = "latest"
	}
	if r.Progress == nil {
		r.Progress = io.Discard
	}
	assetName := "k0smos-metal-" + r.Arch + ".qcow2"

	fmt.Fprintf(r.Progress, "resolving k0smos GitHub release %s for %s\n", r.Release, r.Arch)
	release, err := r.fetchRelease(ctx)
	if err != nil {
		if cached, cacheErr := r.cachedFallback(assetName); cacheErr == nil {
			fmt.Fprintf(r.Progress, "GitHub is unavailable; using cached %s\n", cached)
			return cached, nil
		}
		return "", err
	}
	asset, checksum, err := releaseArtifacts(release, assetName)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(r.CacheDir, url.PathEscape(release.Tag))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	imagePath := filepath.Join(dir, assetName)
	checksumPath := imagePath + ".sha256"

	if err := r.ensureDownloaded(ctx, checksum.source(r.Token != ""), checksumPath); err != nil {
		return "", fmt.Errorf("download checksum: %w", err)
	}
	want, err := readChecksum(checksumPath)
	if err != nil {
		// A truncated cache entry should heal just like a truncated image. Release
		// assets are immutable, so replacing this file cannot change a valid pin.
		_ = os.Remove(checksumPath)
		if downloadErr := r.download(ctx, checksum.source(r.Token != ""), checksumPath); downloadErr != nil {
			return "", fmt.Errorf("replace invalid checksum: %w", downloadErr)
		}
		want, err = readChecksum(checksumPath)
		if err != nil {
			return "", err
		}
	}
	if got, err := fileChecksum(imagePath); err == nil && got == want {
		fmt.Fprintf(r.Progress, "using cached k0smos %s artifact %s\n", release.Tag, imagePath)
		if r.Release == "latest" {
			_ = writeLatest(r.CacheDir, release.Tag)
		}
		return imagePath, nil
	}

	fmt.Fprintf(r.Progress, "downloading k0smos %s %s to %s\n", release.Tag, assetName, imagePath)
	if err := r.download(ctx, asset.source(r.Token != ""), imagePath); err != nil {
		return "", err
	}
	got, err := fileChecksum(imagePath)
	if err != nil {
		return "", err
	}
	if got != want {
		_ = os.Remove(imagePath)
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, got, want)
	}
	if r.Release == "latest" {
		if err := writeLatest(r.CacheDir, release.Tag); err != nil {
			return "", err
		}
	}
	fmt.Fprintf(r.Progress, "verified sha256:%s\n", got)
	return imagePath, nil
}

func (r artifactResolver) fetchRelease(ctx context.Context) (githubRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, releaseAPITimeout)
	defer cancel()
	endpoint := strings.TrimRight(r.API, "/") + "/repos/" + r.Repo + "/releases/latest"
	if r.Release != "latest" {
		endpoint = strings.TrimRight(r.API, "/") + "/repos/" + r.Repo + "/releases/tags/" + url.PathEscape(r.Release)
	}
	var release githubRelease
	if err := r.getJSON(ctx, endpoint, &release); err != nil {
		return release, fmt.Errorf("resolve GitHub release %q: %w", r.Release, err)
	}
	if release.Tag == "" {
		return release, errors.New("GitHub release response has no tag_name")
	}
	return release, nil
}

func (r artifactResolver) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := r.request(ctx, endpoint)
	if err != nil {
		return err
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		hint := ""
		if resp.StatusCode == http.StatusNotFound {
			hint = "; if the repository is private, set GITHUB_TOKEN or GH_TOKEN"
		}
		return fmt.Errorf("GitHub returned %s: %s%s", resp.Status, strings.TrimSpace(string(body)), hint)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func releaseArtifacts(release githubRelease, imageName string) (releaseAsset, releaseAsset, error) {
	var image, checksum releaseAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case imageName:
			image = asset
		case imageName + ".sha256":
			checksum = asset
		}
	}
	if (image.URL == "" && image.APIURL == "") || (checksum.URL == "" && checksum.APIURL == "") {
		return image, checksum, fmt.Errorf("GitHub release %s does not contain %s and %s.sha256; pass --image, or publish a release with the current workflow", release.Tag, imageName, imageName)
	}
	return image, checksum, nil
}

func (a releaseAsset) source(authenticated bool) string {
	if authenticated && a.APIURL != "" {
		return a.APIURL
	}
	return a.URL
}

func (r artifactResolver) ensureDownloaded(ctx context.Context, source, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return r.download(ctx, source, path)
}

func (r artifactResolver) download(ctx context.Context, source, path string) error {
	req, err := r.request(ctx, source)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: server returned %s", source, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (r artifactResolver) request(ctx context.Context, endpoint string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "k0smosctl/"+version)
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	return req, nil
}

func readChecksum(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return "", fmt.Errorf("invalid sha256 file %s", path)
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", fmt.Errorf("invalid sha256 file %s: %w", path, err)
	}
	return strings.ToLower(fields[0]), nil
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeLatest(cacheDir, tag string) error {
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cacheDir, ".latest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := fmt.Fprintln(tmp, tag); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(cacheDir, "latest"))
}

func (r artifactResolver) cachedFallback(assetName string) (string, error) {
	tag := r.Release
	if tag == "latest" {
		b, err := os.ReadFile(filepath.Join(r.CacheDir, "latest"))
		if err != nil {
			return "", err
		}
		tag = strings.TrimSpace(string(b))
	}
	path := filepath.Join(r.CacheDir, url.PathEscape(tag), assetName)
	want, err := readChecksum(path + ".sha256")
	if err != nil {
		return "", err
	}
	got, err := fileChecksum(path)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", errors.New("cached artifact checksum mismatch")
	}
	return path, nil
}
