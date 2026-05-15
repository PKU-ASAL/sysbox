package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Substrate is the Firecracker microVM implementation of substrate.Substrate.
type Substrate struct {
	firecrackerBin string
	jailerBin      string
	kernelPath     string
	rootfsDir      string // base directory for per-VM rootfs copies

	// nextCID is a monotonic allocator for the per-VM guest_cid field.
	// vsock CIDs 0/1/2 are reserved (HYPERVISOR, LOCAL, HOST) so we start at 3.
	nextCID atomic.Uint32
}

// allocCID returns a unique vsock CID for the next VM.
func (s *Substrate) allocCID() uint32 {
	v := s.nextCID.Add(1)
	return 2 + v // start at 3
}

// New creates a Firecracker substrate.
// kernelPath is the path to the uncompressed vmlinux binary.
// rootfsDir is where per-VM rootfs copies are stored.
func New(kernelPath, rootfsDir string) *Substrate {
	// Resolve firecracker binary path.
	fcBin := "firecracker"
	if p, err := exec.LookPath("firecracker"); err == nil {
		fcBin = p
	}
	// Check common locations.
	for _, candidate := range []string{
		os.Getenv("SYSBOX_FC_BIN"),
		filepath.Join(os.Getenv("HOME"), ".local/bin/firecracker"),
		"/usr/local/bin/firecracker",
	} {
		if candidate != "" {
			if _, err := os.Stat(candidate); err == nil {
				fcBin = candidate
				break
			}
		}
	}

	return &Substrate{
		firecrackerBin: fcBin,
		jailerBin:      "jailer",
		kernelPath:     kernelPath,
		rootfsDir:      rootfsDir,
	}
}

func (s *Substrate) Name() string { return "firecracker" }

func (s *Substrate) Capabilities() substrate.Capabilities {
	return substrate.Capabilities{
		SharedKernel:    false,
		SupportsWindows: false,
		BootTime:        "ms",
		NICType:         "tap",
	}
}

var _ substrate.Substrate = (*Substrate)(nil)
