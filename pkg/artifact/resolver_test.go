package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestResolveLocalPath_NoSHA(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "blob")
	mustWriteFile(t, src, []byte("hello"))

	r := &Resolver{CacheDir: filepath.Join(tmp, "cache")}
	res, err := r.Resolve(Spec{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != src {
		t.Fatalf("want %s, got %s", src, res.Path)
	}
	if res.FromCache {
		t.Fatal("local path should not be FromCache")
	}
	if res.SHA256 != hashOf([]byte("hello")) {
		t.Fatalf("sha mismatch: %s", res.SHA256)
	}
}

func TestResolveLocalPath_WithSHA_Valid(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "blob")
	mustWriteFile(t, src, []byte("hello"))

	r := &Resolver{CacheDir: filepath.Join(tmp, "cache")}
	_, err := r.Resolve(Spec{Source: src, SHA256: hashOf([]byte("hello"))})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveLocalPath_WithSHA_Mismatch(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "blob")
	mustWriteFile(t, src, []byte("hello"))

	r := &Resolver{CacheDir: filepath.Join(tmp, "cache")}
	_, err := r.Resolve(Spec{Source: src, SHA256: hashOf([]byte("world"))})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch, got %v", err)
	}
}

func TestResolveLocalPath_Missing(t *testing.T) {
	tmp := t.TempDir()
	r := &Resolver{CacheDir: filepath.Join(tmp, "cache")}
	_, err := r.Resolve(Spec{Source: filepath.Join(tmp, "nope")})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestResolveHTTP_FetchAndCache(t *testing.T) {
	payload := []byte("vmlinux-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, strings.NewReader(string(payload)))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	r := &Resolver{CacheDir: filepath.Join(tmp, "cache"), HTTPClient: srv.Client()}

	// First fetch: download.
	res1, err := r.Resolve(Spec{Source: srv.URL + "/vmlinux", SHA256: hashOf(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if res1.FromCache {
		t.Fatal("first fetch should not be FromCache")
	}
	if _, err := os.Stat(res1.Path); err != nil {
		t.Fatalf("expected cached file at %s: %v", res1.Path, err)
	}

	// Second fetch: should hit cache (we kill the server to be sure).
	srv.Close()
	res2, err := r.Resolve(Spec{Source: srv.URL + "/vmlinux", SHA256: hashOf(payload)})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.FromCache {
		t.Fatal("second fetch should be FromCache")
	}
	if res1.Path != res2.Path {
		t.Fatalf("cache paths diverged: %s vs %s", res1.Path, res2.Path)
	}
}

func TestResolveHTTP_SHAMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "real-data")
	}))
	defer srv.Close()

	tmp := t.TempDir()
	r := &Resolver{CacheDir: filepath.Join(tmp, "cache"), HTTPClient: srv.Client()}
	_, err := r.Resolve(Spec{Source: srv.URL + "/vmlinux", SHA256: hashOf([]byte("not-this"))})
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha256 mismatch, got %v", err)
	}
	// Tempfile should have been cleaned up.
	entries, _ := os.ReadDir(filepath.Join(tmp, "cache"))
	for _, e := range entries {
		sub, _ := os.ReadDir(filepath.Join(tmp, "cache", e.Name()))
		for _, f := range sub {
			if strings.HasPrefix(f.Name(), ".dl-") {
				t.Fatalf("orphan temp file: %s", f.Name())
			}
			if !strings.HasPrefix(f.Name(), ".dl-") {
				// installed file shouldn't exist on mismatch
				t.Fatalf("unexpected cached file after mismatch: %s", f.Name())
			}
		}
	}
}

func TestResolveHTTP_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tmp := t.TempDir()
	r := &Resolver{CacheDir: filepath.Join(tmp, "cache"), HTTPClient: srv.Client()}
	_, err := r.Resolve(Spec{Source: srv.URL + "/nope"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestResolve_EmptySource(t *testing.T) {
	_, err := New().Resolve(Spec{})
	if err == nil {
		t.Fatal("expected error for empty source")
	}
}

func TestIsURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/x": true,
		"http://example.com/x":  true,
		"/abs/path":             false,
		"relative/path":         false,
		"file:///abs/path":      false,
	}
	for in, want := range cases {
		if got := IsURL(in); got != want {
			t.Errorf("IsURL(%q) = %v, want %v", in, got, want)
		}
	}
}
