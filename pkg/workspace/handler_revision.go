package workspace

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// relPathSegment matches a single safe path segment within a project file path.
// Unlike validatePathSegment (which guards topology/node/id names and forbids
// dots), project files legitimately contain dots ("main.hcl",
// "playbook.tar.gz"), so this alphabet is wider. validateRelPath rejects "."
// and ".." segments outright before this regex is consulted.
var relPathSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validateRelPath rejects any path that could escape the project root. Every
// segment must be non-empty, not "." or "..", free of backslashes, and not an
// absolute path.
func ValidateRelPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.ContainsRune(p, '\\') {
		return fmt.Errorf("invalid project path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid project path %q", p)
		}
		if !relPathSegment.MatchString(seg) {
			return fmt.Errorf("invalid project path %q", p)
		}
	}
	return nil
}
