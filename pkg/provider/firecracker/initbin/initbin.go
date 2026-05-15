// Package initbin embeds the compiled sysbox-init binary so the sysbox CLI
// can drop it onto a VM rootfs without external file dependencies.
//
// The binary is produced by `make build-init` (or implicitly by `make build`)
// which cross-compiles `cmd/sysbox-init` for linux/amd64 and writes the result
// to `sysbox-init.bin` in this directory. The placeholder committed to the
// tree is overwritten by every build.
package initbin

import (
	"embed"
	"errors"
	"os"
)

// Build process:
//   1. `make build-init` cross-compiles cmd/sysbox-init to sysbox-init.bin.
//   2. If that file does not exist, the placeholder is embedded instead,
//      and Bytes() reports ErrNotBuilt so callers fail loudly.
//
// Two patterns are used so a fresh `go build ./...` works even before the
// real binary has been produced; the placeholder is intentionally tiny.
//
//go:embed sysbox-init.bin*
var binaryFS embed.FS

var binary = func() []byte {
	if b, err := binaryFS.ReadFile("sysbox-init.bin"); err == nil && len(b) >= 1024 {
		return b
	}
	if b, err := binaryFS.ReadFile("sysbox-init.bin.placeholder"); err == nil {
		return b
	}
	return nil
}()

// ErrNotBuilt is returned when the embedded binary is empty or still the
// placeholder, meaning `make build-init` has not run.
var ErrNotBuilt = errors.New("sysbox-init binary is not built; run `make build-init`")

// Bytes returns the embedded sysbox-init binary (linux/amd64 ELF).
// Returns ErrNotBuilt if the embedded slice is empty.
func Bytes() ([]byte, error) {
	if len(binary) < 1024 {
		return nil, ErrNotBuilt
	}
	return binary, nil
}

// Install copies the embedded binary to `dst` with mode 0755.
func Install(dst string) error {
	b, err := Bytes()
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0755)
}
