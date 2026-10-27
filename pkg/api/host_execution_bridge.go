package api

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// HostExecutionBridge records executor outcomes locally. The canonical run is
// completed separately by the API after verifying the host's signed receipt.
type HostExecutionBridge struct {
	*ExecutionBridge
	hostID string
}

// BindHostIdentity is invoked by the privileged bootstrap before serving. It
// supplies the same public key the host uses to sign stop receipts, allowing
// local continuation validation without copying API issuer configuration.
func (b *HostExecutionBridge) BindHostIdentity(agentID string, public ed25519.PublicKey) error {
	if b == nil || b.ExecutionBridge == nil || b.server == nil || agentID == "" || len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("host identity and public key required")
	}
	if b.hostID != "" && (b.hostID != agentID || !bytes.Equal(b.server.grantHostKeys[agentID], public)) {
		return fmt.Errorf("host identity cannot be rebound")
	}
	if prior := b.server.grantHostKeys[agentID]; len(prior) != 0 && !bytes.Equal(prior, public) {
		return fmt.Errorf("configured host public key conflicts")
	}
	if b.server.grantHostKeys == nil {
		b.server.grantHostKeys = map[string]ed25519.PublicKey{}
	}
	b.server.grantHostKeys[agentID] = append(ed25519.PublicKey(nil), public...)
	b.hostID = agentID
	return nil
}

func NewHostExecutionBridge(cfg config.ServiceConfig) (*HostExecutionBridge, error) {
	bridge := NewExecutionBridge(cfg)
	if bridge.server.initErr != nil {
		return nil, bridge.server.initErr
	}
	return &HostExecutionBridge{ExecutionBridge: bridge}, nil
}

func (b *HostExecutionBridge) Finish(run *controlplane.Run, err error) {
	if run == nil {
		return
	}
	jobs := b.server.jobs
	jobs.mu.Lock()
	run.MarkFinished(err, time.Now())
	run.Inputs = nil
	jobs.runs[run.ID] = run
	jobs.mu.Unlock()
	jobs.logs.Close(run.ID)
}
