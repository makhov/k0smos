package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseServer(t *testing.T, arch string, image []byte) (*httptest.Server, *int) {
	t.Helper()
	downloads := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "k0smos-metal-" + arch + ".qcow2"
		switch r.URL.Path {
		case "/repos/makhov/k0smos/releases/latest":
			_ = json.NewEncoder(w).Encode(githubRelease{Tag: "v1.2.3", Assets: []releaseAsset{
				{Name: name, URL: server.URL + "/assets/" + name},
				{Name: name + ".sha256", URL: server.URL + "/assets/" + name + ".sha256"},
			}})
		case "/assets/" + name:
			downloads++
			_, _ = w.Write(image)
		case "/assets/" + name + ".sha256":
			sum := sha256.Sum256(image)
			fmt.Fprintln(w, hex.EncodeToString(sum[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &downloads
}

func TestReleaseArtifactDownloadsVerifiesAndReusesCache(t *testing.T) {
	image := []byte("firmware bootable qcow2")
	server, downloads := releaseServer(t, "x86_64", image)
	cache := t.TempDir()
	var progress bytes.Buffer
	r := artifactResolver{
		Client: server.Client(), API: server.URL, Repo: "makhov/k0smos",
		CacheDir: cache, Release: "latest", Arch: "x86_64", Progress: &progress,
	}

	first, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || *downloads != 1 {
		t.Fatalf("paths = %q, %q; image downloads = %d, want one cached path/download", first, second, *downloads)
	}
	got, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, image) {
		t.Errorf("cached image = %q, want %q", got, image)
	}
	if !strings.Contains(progress.String(), "verified sha256:") || !strings.Contains(progress.String(), "using cached") {
		t.Errorf("progress did not explain download and reuse:\n%s", progress.String())
	}
}

func TestLatestReleaseFallsBackToVerifiedCacheOffline(t *testing.T) {
	image := []byte("cached qcow2")
	server, _ := releaseServer(t, "aarch64", image)
	cache := t.TempDir()
	r := artifactResolver{
		Client: server.Client(), API: server.URL, Repo: "makhov/k0smos",
		CacheDir: cache, Release: "latest", Arch: "aarch64",
	}
	want, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	var progress bytes.Buffer
	r.Progress = &progress
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.Contains(progress.String(), "GitHub is unavailable") {
		t.Errorf("offline result = %q, progress %q; want cached %q", got, progress.String(), want)
	}
}

func TestReleaseWithoutQCOW2ExplainsHowToProceed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(githubRelease{Tag: "v0.0.1", Assets: []releaseAsset{{Name: "k0smos-x86_64.img.zst", URL: "unused"}}})
	}))
	defer server.Close()
	r := artifactResolver{
		Client: server.Client(), API: server.URL, Repo: "makhov/k0smos",
		CacheDir: t.TempDir(), Release: "latest", Arch: "x86_64",
	}
	_, err := r.Resolve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "v0.0.1") || !strings.Contains(err.Error(), "pass --image") {
		t.Fatalf("missing qcow2 error = %v", err)
	}
}

func TestPrivateReleaseDownloadsThroughAuthenticatedAssetAPI(t *testing.T) {
	image := []byte("private qcow2")
	sum := sha256.Sum256(image)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/makhov/k0smos/releases/latest":
			_ = json.NewEncoder(w).Encode(githubRelease{Tag: "v1.0.0", Assets: []releaseAsset{
				{Name: "k0smos-metal-x86_64.qcow2", APIURL: server.URL + "/api/image"},
				{Name: "k0smos-metal-x86_64.qcow2.sha256", APIURL: server.URL + "/api/checksum"},
			}})
		case "/api/image":
			if r.Header.Get("Accept") != "application/octet-stream" {
				http.Error(w, "wrong accept", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(image)
		case "/api/checksum":
			fmt.Fprintln(w, hex.EncodeToString(sum[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	r := artifactResolver{
		Client: server.Client(), API: server.URL, Repo: "makhov/k0smos",
		CacheDir: t.TempDir(), Release: "latest", Arch: "x86_64", Token: "secret",
	}
	path, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, image) {
		t.Fatalf("private cached image = %q, %v", got, err)
	}
}

func TestArtifactCacheDirHonorsOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifacts")
	got, err := artifactCacheDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("cache dir = %q, want %q", got, dir)
	}
}
