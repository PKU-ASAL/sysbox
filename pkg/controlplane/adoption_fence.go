package controlplane

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// AdoptionDispositionProof attests current ownership after independent fencing.
// It never proves that the original adoption callback returned successfully.
type AdoptionDispositionProof struct {
	Request     HostFenceRequest          `json:"request"`
	Fence       HostFenceReceipt          `json:"fence"`
	Adoption    AdoptionRequest           `json:"adoption"`
	HostBootID  string                    `json:"host_boot_id"`
	ObservedAt  time.Time                 `json:"observed_at"`
	Observation substrate.NodeObservation `json:"observation"`
	Private     json.RawMessage           `json:"private"`
	Signature   []byte                    `json:"signature"`
}

func (p AdoptionDispositionProof) validate() error {
	g := p.Request.Grant
	a := p.Adoption
	digest, err := a.Digest()
	if err != nil {
		return err
	}
	if g.Operation != "adoption_apply" || digest != g.Digest || digest != p.Request.JournalSHA256 || a.ID != g.ActionID || a.AgentID != g.AgentID || a.Topology != p.Request.Topology || g.Scope != (ExecutionScope{TopologyUID: a.TopologyUID, ResourceUID: a.ResourceUID, Generation: a.Generation}) {
		return fmt.Errorf("adoption disposition request mismatch")
	}
	if p.HostBootID != p.Fence.NewBootID || p.ObservedAt.IsZero() || p.ObservedAt.Before(p.Fence.ObservedAt) {
		return fmt.Errorf("adoption disposition requires fresh post-fence observation")
	}
	o := p.Observation
	if !o.Exists || o.ExternalID != a.ExternalID || o.OwnerUID != a.TopologyUID || o.ResourceUID != a.ResourceUID || o.Generation != a.Generation {
		return fmt.Errorf("adoption disposition requires exact target ownership")
	}
	var private map[string]json.RawMessage
	if len(p.Private) > 16<<20 || json.Unmarshal(p.Private, &private) != nil || len(private) == 0 {
		return fmt.Errorf("adoption disposition requires provider recovery state")
	}
	return VerifyHostFenceReceipt(p.Request, p.Fence)
}
func adoptionDispositionMessage(p AdoptionDispositionProof) ([]byte, error) {
	p.Signature = nil
	raw, err := json.Marshal(p)
	return append([]byte("sysbox.adoption-fenced-observation.v1\x00"), raw...), err
}
func SignAdoptionDisposition(key ed25519.PrivateKey, p AdoptionDispositionProof) (AdoptionDispositionProof, error) {
	if len(key) != ed25519.PrivateKeySize {
		return AdoptionDispositionProof{}, fmt.Errorf("invalid host key")
	}
	if err := p.validate(); err != nil {
		return AdoptionDispositionProof{}, err
	}
	raw, err := adoptionDispositionMessage(p)
	if err != nil {
		return AdoptionDispositionProof{}, err
	}
	p.Signature = ed25519.Sign(key, raw)
	return p, nil
}
func VerifyAdoptionDisposition(key ed25519.PublicKey, p AdoptionDispositionProof) error {
	if err := p.validate(); err != nil {
		return err
	}
	raw, err := adoptionDispositionMessage(p)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, raw, p.Signature) {
		return fmt.Errorf("invalid adoption disposition signature")
	}
	return nil
}
