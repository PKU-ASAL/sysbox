// Package artifact resolves binary artifacts referenced from HCL (kernels,
// rootfs images, anything else that ships as an opaque blob) into a local
// filesystem path, fetching and caching as needed.
//
// Supported source schemes:
//
//   - "https://..." / "http://..."  HTTP GET into the cache
//   - "/abs/path"                   local absolute path (no copy)
//   - "relative/path"               local relative path (resolved from cwd)
//
// The resolver verifies sha256 when supplied. Without a sha256 the URL itself
// is hashed to derive a cache key, so re-downloads only happen when the URL
// changes.
//
// Cache layout:
//
//	$XDG_CACHE_HOME/sysbox/artifacts/<sha-or-urlhash>/<basename>
//
// Cache entries are content-addressed and shared across runs and across
// resources; `sysbox destroy` does not remove them.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/config"
)

// Spec describes a single artifact reference.
type Spec struct {
	// Source is one of: HTTP(S) URL, absolute path, or relative path.
	Source string
	// SHA256 is the expected hex digest. Empty means "trust the source".
	SHA256 string
}

// Result is what Resolve returns: the on-disk path the caller should use,
// plus metadata describing how it was obtained.
type Result struct {
	// Path is the absolute on-disk path of the resolved artifact.
	Path string
	// Source is the original Spec.Source.
	Source string
	// SHA256 is the verified (or computed) digest of the artifact contents.
	SHA256 string
	// FromCache is true when the artifact was served out of the cache.
	FromCache bool
}

// Resolver fetches artifacts on demand.
type Resolver struct {
	// CacheDir holds downloaded artifacts. Defaults to
	// $SYSBOX_CACHE/artifacts when set, then the user cache fallback.
	CacheDir string

	// HTTPClient is used for URL fetches. Defaults to a 30-minute-timeout
	// client. Tests may swap this out.
	HTTPClient *http.Client
}

// New returns a Resolver using the default cache dir and HTTP client.
func New() *Resolver {
	return &Resolver{
		CacheDir:   DefaultCacheDir(),
		HTTPClient: &http.Client{Timeout: 30 * time.Minute},
	}
}

// DefaultCacheDir resolves the standard sysbox artifact cache location.
func DefaultCacheDir() string {
	if v := os.Getenv("SYSBOX_CACHE"); v != "" {
		return filepath.Join(v, "artifacts")
	}
	if v := os.Getenv("SYSBOX_CACHE_DIR"); v != "" {
		return filepath.Join(v, "artifacts")
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "sysbox", "artifacts")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "sysbox", "artifacts")
	}
	return filepath.Join(home, ".cache", "sysbox", "artifacts")
}

func DefaultRootCacheDir() string {
	return filepath.Join(config.SysboxCache(), "rootfs")
}

// Resolve returns the local path of the artifact described by spec,
// downloading it into the cache when necessary.
func (r *Resolver) Resolve(spec Spec) (Result, error) {
	if spec.Source == "" {
		return Result{}, fmt.Errorf("artifact: empty source")
	}

	switch scheme(spec.Source) {
	case "http", "https":
		return r.resolveHTTP(spec)
	case "file", "":
		return r.resolveLocal(spec)
	default:
		return Result{}, fmt.Errorf("artifact: unsupported source scheme in %q", spec.Source)
	}
}

func (r *Resolver) resolveLocal(spec Spec) (Result, error) {
	p := strings.TrimPrefix(spec.Source, "file://")
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return Result{}, fmt.Errorf("artifact: resolve abs path %q: %w", p, err)
		}
		p = abs
	}
	st, err := os.Stat(p)
	if err != nil {
		return Result{}, fmt.Errorf("artifact: %w", err)
	}
	if st.IsDir() {
		return Result{}, fmt.Errorf("artifact: %q is a directory, expected a file", p)
	}
	sum, err := sha256File(p)
	if err != nil {
		return Result{}, err
	}
	if spec.SHA256 != "" && !strings.EqualFold(sum, spec.SHA256) {
		return Result{}, fmt.Errorf("artifact: sha256 mismatch for %s: have %s, want %s", p, sum, spec.SHA256)
	}
	return Result{Path: p, Source: spec.Source, SHA256: sum, FromCache: false}, nil
}

func (r *Resolver) resolveHTTP(spec Spec) (Result, error) {
	key := spec.SHA256
	if key == "" {
		// URL-derived key: deterministic but not content-addressed. When the
		// user supplies sha256 we use that instead so the cache is shared
		// across mirrors of the same blob.
		h := sha256.Sum256([]byte(spec.Source))
		key = hex.EncodeToString(h[:])
	}
	basename := path.Base(urlPath(spec.Source))
	if basename == "" || basename == "/" || basename == "." {
		basename = "artifact"
	}
	dir := filepath.Join(r.CacheDir, key)
	dst := filepath.Join(dir, basename)

	// Cache hit?
	if st, err := os.Stat(dst); err == nil && !st.IsDir() {
		if spec.SHA256 != "" {
			sum, err := sha256File(dst)
			if err != nil {
				return Result{}, err
			}
			if !strings.EqualFold(sum, spec.SHA256) {
				// Cached file is corrupt: re-download.
				_ = os.Remove(dst)
			} else {
				return Result{Path: dst, Source: spec.Source, SHA256: sum, FromCache: true}, nil
			}
		} else {
			sum, err := sha256File(dst)
			if err != nil {
				return Result{}, err
			}
			return Result{Path: dst, Source: spec.Source, SHA256: sum, FromCache: true}, nil
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("artifact: mkdir cache: %w", err)
	}

	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Get(spec.Source)
	if err != nil {
		return Result{}, fmt.Errorf("artifact: GET %s: %w", spec.Source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("artifact: GET %s: status %s", spec.Source, resp.Status)
	}

	tmp, err := os.CreateTemp(dir, ".dl-*")
	if err != nil {
		return Result{}, fmt.Errorf("artifact: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return Result{}, fmt.Errorf("artifact: download %s: %w", spec.Source, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return Result{}, err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if spec.SHA256 != "" && !strings.EqualFold(sum, spec.SHA256) {
		_ = os.Remove(tmpPath)
		return Result{}, fmt.Errorf("artifact: sha256 mismatch for %s: have %s, want %s", spec.Source, sum, spec.SHA256)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return Result{}, fmt.Errorf("artifact: install %s: %w", dst, err)
	}
	return Result{Path: dst, Source: spec.Source, SHA256: sum, FromCache: false}, nil
}

func scheme(src string) string {
	idx := strings.Index(src, "://")
	if idx < 0 {
		return ""
	}
	return strings.ToLower(src[:idx])
}

func urlPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Path
}

func sha256File(path string) (string, error) {
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

// IsURL reports whether src looks like a remote URL we should fetch
// (vs a local file). Useful for callers that want to detect URL inputs
// without invoking Resolve.
func IsURL(src string) bool {
	s := scheme(src)
	return s == "http" || s == "https"
}
