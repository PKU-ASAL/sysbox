package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Substrate is the Firecracker microVM implementation of substrate.Substrate.
type Substrate struct {
	substrate.BaseSubstrate // inherits Validate / DecodeProviderConfig defaults

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
		NICHotPlug:      false, // firecracker requires NICs declared in boot config
		DiskHotPlug:     false,
		NICKinds:        []string{substrate.NICKindTap},
		ConsoleKinds:    []string{substrate.ConsoleKindSerial},
		NeedsCloudinit:  false, // sysbox-init + config drive replaces cloud-init
		PIDVisibility:   substrate.PIDVisibilityOpaque,
		SupportsPause:   false, // not wired yet
		BootTime:        150 * time.Millisecond,
		Notes:           "microVM via Firecracker; NICs cold-plug only; in-guest agent via vsock-rpc.",
	}
}

// Validate ensures the spec carries what the firecracker substrate needs.
func (s *Substrate) Validate(spec substrate.NodeSpec) error {
	// Firecracker needs either an image with rootfs metadata (resolved later
	// in PrepareImage) or an explicit Rootfs override; kernel is optional
	// because the substrate has a default kernel configured at New() time.
	_ = spec // PR-01 stub: accept anything; tighter checks land in PR-05.
	return nil
}

var _ substrate.Substrate = (*Substrate)(nil)
