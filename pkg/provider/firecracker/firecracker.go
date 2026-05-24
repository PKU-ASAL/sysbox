package firecracker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	providerexec "github.com/oslab/sysbox/pkg/provider/exec"

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
	cfg := config.MustLoadServiceConfig("")
	for _, candidate := range []string{
		cfg.Providers.Firecracker.Binary,
		filepath.Join(cfg.Paths.Cache, "tools", "firecracker"),
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

func (s *Substrate) PreflightChecks(required bool) []substrate.PreflightCheck {
	return substrate.FirecrackerPreflightChecks(required)
}

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
		SupportsPause:   true, // SIGSTOP/SIGCONT on FC process
		BootTime:        150 * time.Millisecond,
		Notes:           "microVM via Firecracker; NICs cold-plug only; in-guest agent via vsock-rpc.",
	}
}

// Validate ensures the spec carries what the firecracker substrate needs.
func (s *Substrate) Validate(spec substrate.NodeSpec) error {
	// FC requires rootfs-based images (not docker_ref) and a kernel.
	if spec.ProviderConfig != nil {
		cfg, ok := spec.ProviderConfig.(*Config)
		if !ok {
			return substrate.NewValidationError("firecracker: wrong provider config type %T", spec.ProviderConfig)
		}
		if cfg.Kernel == "" && s.kernelPath == "" {
			return substrate.NewValidationError("firecracker: kernel is required (set provider \"firecracker\" { kernel = ... } in HCL)")
		}
	}
	return nil
}

// MarshalProviderState writes the firecracker HandleState as JSON.
func (s *Substrate) MarshalProviderState(h substrate.NodeHandle) (json.RawMessage, error) {
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs == nil {
		return nil, nil
	}
	return json.Marshal(hs)
}

// UnmarshalProviderState restores HandleState from a previously persisted blob.
func (s *Substrate) UnmarshalProviderState(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var hs HandleState
	if err := json.Unmarshal(data, &hs); err != nil {
		return nil, fmt.Errorf("firecracker: unmarshal handle state: %w", err)
	}
	return &hs, nil
}

// Connection returns a vsock or SSH connection for the VM. Prefers vsock when
// the handle advertises it (sysbox-init present); falls back to SSH otherwise.
func (s *Substrate) Connection(handle substrate.NodeHandle, hints []substrate.ConnectionHint) (substrate.Connection, error) {
	hs, _ := handle.Provider.(*HandleState)
	// Explicit HCL type overrides auto-selection.
	if len(hints) > 0 && hints[0].Type != "" && hints[0].Type != "auto" {
		switch hints[0].Type {
		case "vsock":
			if hs == nil || hs.VsockUDS == "" {
				return nil, fmt.Errorf("vsock connection requested but VM has no vsock channel")
			}
			return providerexec.NewVsockConnection(hs.VsockUDS, hs.VsockPort), nil
		case "ssh":
			return s.sshConn(handle, hints), nil
		}
	}
	// Auto: prefer vsock, fall back to SSH.
	if hs != nil && hs.VsockUDS != "" {
		return providerexec.NewVsockConnection(hs.VsockUDS, hs.VsockPort), nil
	}
	return s.sshConn(handle, hints), nil
}

// sshConn builds an SSH connection from the handle state + optional HCL hints.
func (s *Substrate) sshConn(handle substrate.NodeHandle, hints []substrate.ConnectionHint) substrate.Connection {
	hs, _ := handle.Provider.(*HandleState)
	host := handle.Net.PrimaryIP
	port := "22"
	user := "root"
	pass := "root"
	key := ""
	if hs != nil {
		if hs.SSHIP != "" {
			host = hs.SSHIP
		}
		if hs.SSHPort != "" {
			port = hs.SSHPort
		}
	}
	if len(hints) > 0 {
		h := hints[0]
		if h.Host != "" {
			host = h.Host
		}
		if h.User != "" {
			user = h.User
		}
		if h.Password != "" {
			pass = h.Password
		}
		if h.PrivateKey != "" {
			key = h.PrivateKey
		}
	}
	return providerexec.NewSSHConnectionWithPort(host, port, user, key, pass)
}

var _ substrate.Substrate = (*Substrate)(nil)
