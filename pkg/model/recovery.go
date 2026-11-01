package model

import (
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type RecoveredResource struct {
	Discovered              *state.Resource            `json:"discovered,omitempty"`
	AttachmentCount         int                        `json:"attachment_count,omitempty"`
	AttachmentResidueAbsent bool                       `json:"attachment_residue_absent,omitempty"`
	SSHCommitted            *substrate.SSHAccessResult `json:"ssh_committed,omitempty"`
	ResetSSH                []RecoveredResource        `json:"reset_ssh,omitempty"`
	ResetSource             *RecoveredResource         `json:"reset_source,omitempty"`
	ResetReplacement        *RecoveredResource         `json:"reset_replacement,omitempty"`
	DeleteResidueAbsent     bool                       `json:"delete_residue_absent,omitempty"`
	ProviderScope           string                     `json:"provider_scope,omitempty"`
	ActionID                string                     `json:"action_id"`
	Resource                string                     `json:"resource"`
	OwnerUID                string                     `json:"owner_uid"`
	ResourceUID             string                     `json:"resource_uid"`
	Generation              int64                      `json:"generation"`
	ExternalID              string                     `json:"external_id,omitempty"`
	Status                  string                     `json:"status"`
	JournalStatus           string                     `json:"journal_status"`
}
