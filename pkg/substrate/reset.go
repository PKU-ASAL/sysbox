package substrate

type ResetPhase string

const (
	ResetPhasePreparing ResetPhase = "preparing"
	ResetPhasePrepared  ResetPhase = "prepared"
	ResetPhaseApplying  ResetPhase = "applying"
	ResetPhaseComplete  ResetPhase = "complete"
)

type ResetRequest struct {
	Current  NodeHandle
	Node     NodeSpec
	Baseline ArtifactIdentity
}

type ResetHandle struct {
	Provider any
}

type ResetObservation struct {
	Phase          ResetPhase
	Converged      bool
	OldExternalID  string
	NewExternalID  string
	BaselineDigest string
	Reason         string
	Residue        []string
}
