package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const MaxFileInstallSize int64 = 16 << 20
const (
	FileInstallCommitted = "committed"
	FileInstallCancelled = "cancelled"
	FileInstallUnknown   = "unknown"
)

type FileInstallRequest struct {
	RebootScope string `json:"reboot_scope,omitempty"`
	ID          string `json:"id"`
	Path        string `json:"path"`
	Mode        uint32 `json:"mode"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

var fileInstallID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (r FileInstallRequest) Validate() error {
	if r.RebootScope != "" && r.RebootScope != "guest-tmpfs" {
		return fmt.Errorf("unsupported file reboot scope")
	}
	if !fileInstallID.MatchString(r.ID) || !path.IsAbs(r.Path) || path.Clean(r.Path) != r.Path || r.Path == "/" || strings.ContainsRune(r.Path, 0) || r.Mode&^uint32(0777) != 0 || r.Size < 0 || r.Size > MaxFileInstallSize || len(r.SHA256) != 64 || strings.ToLower(r.SHA256) != r.SHA256 {
		return fmt.Errorf("invalid file installation request")
	}
	if _, err := hex.DecodeString(r.SHA256); err != nil {
		return fmt.Errorf("invalid installation SHA256")
	}
	// The guest owns the allowed-root policy. Control-plane paths can never
	// become writable by expanding that policy or by a worker-selected path.
	for _, root := range []string{"/proc", "/sys", "/dev", "/etc/sysbox", "/var/lib/sysbox", "/run/sysbox", "/usr/local/bin/sysbox-guest-task"} {
		if r.Path == root || strings.HasPrefix(r.Path, root+"/") {
			return fmt.Errorf("protected file installation path")
		}
	}
	return nil
}
func (r FileInstallRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type FileInstallHandle struct {
	ID          string `json:"id"`
	Digest      string `json:"digest"`
	BootID      string `json:"boot_id"`
	NodeID      string `json:"node_id"`
	TopologyUID string `json:"topology_uid"`
	ResourceUID string `json:"resource_uid"`
	Generation  int64  `json:"generation"`
	Epoch       uint64 `json:"epoch"`
}
type FileInstallResult struct {
	StoppedEpoch uint64 `json:"stopped_epoch,omitempty"`
	Outcome      string `json:"outcome"`
	Quiescent    bool   `json:"quiescent"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
}
type ConnectionFileInstaller interface {
	InstallFile(context.Context, FileInstallRequest, io.Reader) (FileInstallHandle, FileInstallResult, error)
	ObserveFileInstall(context.Context, FileInstallHandle) (FileInstallResult, error)
	CancelFileInstall(context.Context, FileInstallHandle) (FileInstallResult, error)
}

// InstalledFileInspection describes the current target, not the historical
// installation outcome. It contains no file contents or authority to mutate.
type InstalledFileInspection struct {
	Status string `json:"status"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type ConnectionInstalledFileInspector interface {
	InspectInstalledFile(context.Context, FileInstallHandle) (InstalledFileInspection, error)
}
