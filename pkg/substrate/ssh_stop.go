package substrate

import (
	"context"
	"fmt"
)

// SSHAccessStop separates cessation from a successful key configuration. The
// expected Handle is an identity only; Result exists only for a committed RPC.
type SSHAccessStop struct {
	Handle    SSHAccessHandle  `json:"handle"`
	Quiescent bool             `json:"quiescent"`
	Outcome   string           `json:"outcome"`
	Result    *SSHAccessResult `json:"result,omitempty"`
}

// Validate checks request, boot and epoch; callers additionally bind resource identity.
func (s SSHAccessStop) Validate(epoch uint64, boot string, req SSHAccessMutation) error {
	// Scope identity is checked by callers at their grant/transport boundary.
	digest, err := req.Digest()
	h := s.Handle
	if err != nil || !s.Quiescent || h.ID != req.ID || h.Digest != digest || h.Epoch != epoch || h.BootID != boot || h.TopologyUID == "" || h.ResourceUID == "" || h.Generation < 1 || h.User != req.Config.User || h.AccessID != req.Config.AccessID || req.ExpectedRevision == ^uint64(0) || h.Revision != req.ExpectedRevision+1 {
		return fmt.Errorf("invalid SSH stop evidence")
	}
	switch s.Outcome {
	case "unknown":
		if s.Result != nil {
			return fmt.Errorf("unknown SSH stop has committed result")
		}
	case "committed":
		if s.Result == nil || s.Result.Handle != h || s.Result.Present == req.Delete {
			return fmt.Errorf("SSH committed stop mismatch")
		}
	default:
		return fmt.Errorf("invalid SSH stop outcome")
	}
	return nil
}

type ConnectionSSHStopper interface {
	StopSSHAccess(context.Context, SSHAccessMutation) (SSHAccessStop, error)
}
