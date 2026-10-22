package controlplane

import (
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

func FromFileInstallCommand(cmd AgentCommand) (substrate.FileInstallRequest, error) {
	if cmd.Type != "guest_file_put" || cmd.FilePut == nil || cmd.ID == "" || cmd.AgentID == "" || cmd.ActionID == "" || cmd.FilePut.ID != cmd.ActionID || cmd.TopologyUID == "" || cmd.ResourceUID == "" || cmd.Generation < 1 {
		return substrate.FileInstallRequest{}, fmt.Errorf("file install requires complete dispatch binding")
	}
	p := cmd.FilePut
	r := substrate.FileInstallRequest{RebootScope: p.RebootScope, ID: p.ID, Path: p.Path, Mode: p.Mode, Size: p.Size, SHA256: p.SHA256}
	return r, r.Validate()
}
