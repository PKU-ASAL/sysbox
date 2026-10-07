package api

// Runtime capabilities supplement existing host preflight fields. They describe
// this deployed protocol, not transparent cross-API secret recovery.
type runtimeCapabilities struct {
	preflightResult
	Protocol           string `json:"protocol"`
	DurableApply       bool   `json:"durable_apply"`
	KeyedDestroy       bool   `json:"keyed_destroy"`
	CleanupInspection  bool   `json:"cleanup_inspection"`
	SingleAPIExecution bool   `json:"single_api_execution"`
}

func (s *Server) runtimeCapabilities(preflight preflightResult) runtimeCapabilities {
	durable := false
	switch s.apiStore.(type) {
	case *sqliteAPIStore, *postgresAPIStore:
		durable = s.initErr == nil
	}
	return runtimeCapabilities{preflight, "cyberfield-runtime-v1", durable, durable, true, true}
}
