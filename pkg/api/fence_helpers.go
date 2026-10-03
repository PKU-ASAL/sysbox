package api

import "github.com/oslab/sysbox/pkg/controlplane"

func commandEventMatchesFence(cmd controlplane.AgentCommand, event controlplane.AgentCommandEvent) bool {
	if cmd.ActionID != "" && (event.ActionID != cmd.ActionID || event.ResourceUID != cmd.ResourceUID || event.Generation != cmd.Generation || event.LeaseEpoch != cmd.LeaseEpoch || event.LeaseOwner != cmd.LeaseOwner || event.Attempt != cmd.Attempt) {
		return false
	}
	if event.LeaseOwner != "" && event.LeaseOwner != cmd.LeaseOwner || event.Attempt != 0 && event.Attempt != cmd.Attempt {
		return false
	}
	return true
}
func fenceProofPresent(action, resource string, generation, epoch int64, owner string, attempt int) bool {
	return action != "" || resource != "" || generation != 0 || epoch != 0 || owner != "" || attempt != 0
}
