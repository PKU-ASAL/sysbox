package api

// Protocol capabilities describe this API's guarantees, independently of host
// provider preflight. No downstream application's business policy belongs here.
type runtimeCapabilities struct {
	ClaimReplay            bool   `json:"claim_replay"`
	MultiAPIExecution      bool   `json:"multi_api_execution"`
	ExecutionStoreScope    string `json:"execution_store_scope"`
	RecoveryInputsProtocol int    `json:"recovery_inputs_protocol"`
	preflightResult
	Protocol                  string `json:"protocol"`
	DurableApply              bool   `json:"durable_apply"`
	KeyedDestroy              bool   `json:"keyed_destroy"`
	CleanupInspection         bool   `json:"cleanup_inspection"`
	SingleAPIExecution        bool   `json:"single_api_execution"`
	ExternalAbsenceInspection bool   `json:"external_absence_inspection"`
	ResourceLeaseRevocation   bool   `json:"resource_lease_revocation"`
}

func (s *Server) runtimeCapabilities(preflight preflightResult) runtimeCapabilities {
	durable := s.initErr == nil && s.apiStore != nil
	_, shared := s.apiStore.(*postgresAPIStore)
	scope := "host-local"
	if shared {
		scope = "shared-postgres"
	}
	return runtimeCapabilities{ClaimReplay: durable, MultiAPIExecution: durable && shared, ExecutionStoreScope: scope, RecoveryInputsProtocol: 1, preflightResult: preflight, Protocol: "sysbox-runtime-v1", DurableApply: durable, KeyedDestroy: durable, CleanupInspection: true, SingleAPIExecution: true}
}
