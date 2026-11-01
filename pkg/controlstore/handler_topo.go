package controlstore

import (
	"fmt"
	"regexp"
)

// safeSegment matches allowed path values for topology / node / id URL segments.
// Rejects "..", special characters, and URL-encoded traversals.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// globalRevisionPattern matches the content-addressed digests produced by
// POST /v1/revisions ("sha256:" + 64 lowercase hex chars).
var GlobalRevisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// validatePathSegment returns an error if the segment could enable path traversal.
func ValidatePathSegment(seg, label string) error {
	if !safeSegment.MatchString(seg) {
		return fmt.Errorf("invalid %s %q: must match [A-Za-z0-9_-]+", label, seg)
	}
	return nil
}
