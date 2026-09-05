package controlplane

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"time"
)

// GlobalRevision is a content-addressed project directory tree, decoupled from
// any topology. Files maps a repo-root-relative path (slash-separated) to its
// raw content; the root HCL lives at "field.sysbox.hcl".
//
// CreatedAt is the last-publish time, not the first-seen time: republishing
// identical content overwrites the stored record, so the timestamp records the
// most recent publish of that content.
type GlobalRevision struct {
	Revision  string            `json:"revision"`
	Files     map[string][]byte `json:"files"`
	Size      int               `json:"size"`
	CreatedAt time.Time         `json:"created_at"`
}

// ComputeRevisionDigest hashes an entire project tree with file paths as part
// of the digest. Paths are sorted lexicographically before hashing so map
// iteration order cannot affect the result; each (path, content) pair is
// length-prefixed to prevent concatenation collisions such as {"ab":"c"} vs
// {"a":"bc"}.
func ComputeRevisionDigest(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range paths {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(p)))
		h.Write(lenBuf[:])
		h.Write([]byte(p))
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(files[p])))
		h.Write(lenBuf[:])
		h.Write(files[p])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
