package artifact

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type portableSourcesKey struct{}

// WithPortableSources requires artifacts to be reproducible on an independent
// execution host: revision files or digest-pinned network downloads.
func WithPortableSources(ctx context.Context) context.Context {
	return context.WithValue(ctx, portableSourcesKey{}, true)
}

func ResolveSource(ctx context.Context, source, digest, workspace string) (string, error) {
	portable, _ := ctx.Value(portableSourcesKey{}).(bool)
	if IsURL(source) {
		if portable {
			raw, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
			if err != nil || len(raw) != 32 {
				return "", fmt.Errorf("remote artifact URL requires a SHA-256 digest")
			}
		}
		return source, nil
	}
	if source == "" {
		return "", fmt.Errorf("artifact source is empty")
	}
	if filepath.IsAbs(source) {
		if portable {
			return "", fmt.Errorf("remote artifact cannot use host absolute path %q; bundle a revision-relative file or use a digest-pinned URL", source)
		}
		return source, nil
	}
	if !portable {
		if workspace == "" {
			return source, nil
		}
		return filepath.Join(workspace, source), nil
	}
	if workspace == "" || !filepath.IsLocal(source) {
		return "", fmt.Errorf("artifact must be inside the immutable revision")
	}
	// Published revision trees prohibit symlinks. Recheck the entire path before
	// consuming it so a modified local cache cannot escape that boundary.
	path := workspace
	for _, part := range strings.Split(filepath.Clean(source), string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("artifact revision path contains a symlink")
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact revision path must be a regular file")
	}
	return path, nil
}
