// Package initbin embeds compiled sysbox-init binaries (one per supported
// guest architecture) so the sysbox CLI can drop the right one onto a VM
// rootfs without external file dependencies.
//
// Build process:
//   - make build uses scripts/release/build-binaries.sh to compile both guest
//     helpers in a temporary directory and embed them through a Go overlay.
//   - make build-all writes matching Linux bundles under bin/linux-{amd64,arm64}.
//   - Direct go build uses the tracked placeholders and fails explicitly if a
//     caller attempts to install a helper. Acceptance tests use the overlay wrapper.
//
// Firecracker does not emulate CPUs, so the guest arch always equals the
// host arch. Default Install() picks runtime.GOARCH, but callers can use
// InstallFor(arch) to install a specific architecture explicitly.
package initbin

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"runtime"
)

//go:embed sysbox-init.linux-*.bin
var binaryFS embed.FS

// ErrNotBuilt means this build still embeds placeholders.
var ErrNotBuilt = errors.New("sysbox-init binary is not built for this arch; run make build or scripts/release/with-helper-overlay.sh")

// supportedArches is the canonical list of embedded guest architectures.
var supportedArches = []string{"amd64", "arm64"}

// bytesFor returns the embedded binary for `goarch` (e.g. "amd64", "arm64").
// Falls back to the placeholder if nothing was built for that arch.
func bytesFor(goarch string) ([]byte, error) {
	name := fmt.Sprintf("sysbox-init.linux-%s.bin", goarch)
	if b, err := binaryFS.ReadFile(name); err == nil && len(b) >= 1024 {
		return b, nil
	}
	return nil, ErrNotBuilt
}

// Bytes returns the embedded sysbox-init binary for the host's GOARCH.
// Callers that need a different architecture should use BytesFor.
func Bytes() ([]byte, error) {
	return bytesFor(runtime.GOARCH)
}

// BytesFor returns the embedded sysbox-init binary for the given GOARCH.
func BytesFor(goarch string) ([]byte, error) {
	return bytesFor(goarch)
}

// Install copies the host-arch binary to `dst` with mode 0755.
func Install(dst string) error {
	return InstallFor(runtime.GOARCH, dst)
}

// InstallFor copies the binary for `goarch` to `dst` with mode 0755.
func InstallFor(goarch, dst string) error {
	b, err := bytesFor(goarch)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0755)
}

// Available reports which architectures have a real (non-placeholder) binary
// embedded in this build.
func Available() []string {
	var got []string
	for _, a := range supportedArches {
		if _, err := bytesFor(a); err == nil {
			got = append(got, a)
		}
	}
	return got
}
