package commands

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/hostsupervisor"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/spf13/cobra"
)

// The controller is the trust boundary: this offline signer validates binding
// and evidence structure, not the physical truth of an arbitrary JSON report.
func newHostFenceCommand() *cobra.Command {
	root := &cobra.Command{Use: "host-fence", Short: "Inspect and attest disposable host fencing evidence"}
	root.AddCommand(&cobra.Command{Use: "request-digest <admission.json>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		record, err := readFenceAdmission(args[0])
		if err != nil {
			return err
		}
		digest, err := record.Request.Digest()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), digest)
		return err
	}})
	var keyPath string
	attest := &cobra.Command{Use: "attest <admission.json> <evidence.json>", Short: "Sign evidence with the independently enrolled controller key", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		record, err := readFenceAdmission(args[0])
		if err != nil {
			return err
		}
		var evidence fenceAcceptanceEvidence
		if err = readFenceJSON(args[1], &evidence); err != nil {
			return err
		}
		receipt, err := evidence.receipt(record)
		if err != nil {
			return err
		}
		// O_NOFOLLOW and fstat bind permission checks to the descriptor actually read.
		fd, err := syscall.Open(keyPath, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), keyPath)
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("controller key must be a private regular file")
		}
		key, err := io.ReadAll(io.LimitReader(file, ed25519.PrivateKeySize+1))
		if err != nil {
			return err
		}
		signed, err := controlplane.SignHostFenceReceipt(key, record.Request, receipt)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(signed)
	}}
	attest.Flags().StringVar(&keyPath, "key", "", "raw Ed25519 controller private key (0600)")
	_ = attest.MarkFlagRequired("key")
	root.AddCommand(attest)
	var gcSocket string
	inspectGC := &cobra.Command{Use: "inspect-gc <fence-id>", Short: "Obtain a signed read-only GC observation after admitted host fencing", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		proof, err := hostsupervisor.NewTaskClient(gcSocket, hostsupervisor.TargetRef{}).InspectGCDisposition(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(proof)
	}}
	inspectGC.Flags().StringVar(&gcSocket, "socket", "", "local host-supervisor Unix socket")
	_ = inspectGC.MarkFlagRequired("socket")
	root.AddCommand(inspectGC)
	return root
}

func readFenceJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return fmt.Errorf("fence input must be a regular JSON file at most 64KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("one JSON object required")
	}
	return nil
}
func readFenceAdmission(path string) (persistence.HostFenceRecord, error) {
	var record persistence.HostFenceRecord
	if err := readFenceJSON(path, &record); err != nil {
		return record, err
	}
	if record.AcceptedAt.IsZero() || record.BarrierID != "fence-"+record.Request.ID || record.Receipt != nil {
		return record, fmt.Errorf("uncompleted admitted fence request required")
	}
	return record, record.Request.Validate()
}

type fenceAcceptanceEvidence struct {
	RequestSHA256       string    `json:"request_sha256"`
	LabID               string    `json:"lab_id"`
	Role                string    `json:"role"`
	OldBoot             string    `json:"old_boot"`
	NewBoot             string    `json:"new_boot"`
	ControllerBoot      string    `json:"controller_boot"`
	ControllerUnchanged bool      `json:"controller_boot_unchanged"`
	ControlUnchanged    bool      `json:"control_boot_unchanged"`
	PID                 int       `json:"old_qemu_pid"`
	Start               string    `json:"old_qemu_start"`
	Exited              bool      `json:"qemu_exited"`
	Disk                []uint64  `json:"disk_device_inode"`
	Scope               string    `json:"provider_scope"`
	Result              string    `json:"operation_result"`
	Status              string    `json:"status"`
	ObservedAt          time.Time `json:"observed_at"`
	Provider            struct {
		ExternalStorage *bool  `json:"external_storage"`
		DockerExec      bool   `json:"docker_exec_inflight"`
		LibvirtEndpoint string `json:"libvirt_endpoint"`
	} `json:"provider"`
	PostBoot []string `json:"post_boot_provider_state"`
}

func (v fenceAcceptanceEvidence) receipt(record persistence.HostFenceRecord) (controlplane.HostFenceReceipt, error) {
	if v.Status != "fenced" || v.Role != "execution" || !v.ControlUnchanged || len(v.Disk) != 2 || v.Provider.ExternalStorage == nil || *v.Provider.ExternalStorage || !v.Provider.DockerExec || v.Provider.LibvirtEndpoint != "qemu:///system" || len(v.PostBoot) != 2 || v.PostBoot[0] != "false" || v.PostBoot[1] != "shut off" || v.ObservedAt.Before(record.AcceptedAt) || v.ObservedAt.After(time.Now().UTC().Add(controlplane.ExecutionGrantMaxClockSkew)) {
		return controlplane.HostFenceReceipt{}, fmt.Errorf("incomplete pre-admitted local provider fencing evidence")
	}
	return controlplane.HostFenceReceipt{Version: 1, RequestSHA256: v.RequestSHA256, LabID: v.LabID, OldBootID: v.OldBoot, NewBootID: v.NewBoot, ControllerBootID: v.ControllerBoot, ControllerBootUnchanged: v.ControllerUnchanged, QEMUPID: v.PID, QEMUStart: v.Start, QEMUExited: v.Exited, DiskDevice: v.Disk[0], DiskInode: v.Disk[1], ProviderScope: v.Scope, OperationResult: v.Result, ObservedAt: v.ObservedAt}, nil
}
