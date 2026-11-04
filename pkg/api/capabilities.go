package api

import (
	"github.com/oslab/sysbox/pkg/buildinfo"
	"github.com/oslab/sysbox/pkg/controlstore"
	"github.com/oslab/sysbox/pkg/runtime"
)

// Protocol capabilities describe this API's guarantees, independently of host
// provider preflight. No downstream application's business policy belongs here.
type runtimeCapabilities struct {
	ClaimReplay            bool   `json:"claim_replay"`
	MultiAPIExecution      bool   `json:"multi_api_execution"`
	ExecutionStoreScope    string `json:"execution_store_scope"`
	RecoveryInputsProtocol int    `json:"recovery_inputs_protocol"`
	runtime.PreflightResult
	Protocol                  string `json:"protocol"`
	DurableApply              bool   `json:"durable_apply"`
	KeyedDestroy              bool   `json:"keyed_destroy"`
	CleanupInspection         bool   `json:"cleanup_inspection"`
	SingleAPIExecution        bool   `json:"single_api_execution"`
	ExternalAbsenceInspection bool   `json:"external_absence_inspection"`
	ResourceLeaseRevocation   bool   `json:"resource_lease_revocation"`
	BuildVersion              string `json:"build_version"`
	BuildCommit               string `json:"build_commit"`
}

func (s *Server) runtimeCapabilities(preflight runtime.PreflightResult) runtimeCapabilities {
	durable := s.initErr == nil && s.apiStore != nil
	_, shared := s.apiStore.(*controlstore.Postgres)
	scope := "host-local"
	if shared {
		scope = "shared-postgres"
	}
	return runtimeCapabilities{ClaimReplay: durable, MultiAPIExecution: durable && shared, ExecutionStoreScope: scope, RecoveryInputsProtocol: 1, PreflightResult: preflight, Protocol: "sysbox-runtime-v1", DurableApply: durable, KeyedDestroy: durable, CleanupInspection: true, SingleAPIExecution: true, BuildVersion: buildinfo.Version, BuildCommit: buildinfo.Commit}
}
