package substrate

import (
	"context"
	"fmt"
)

// OperationIdentity binds a provider side effect to a durable action and
// resource generation. Display names and process-local handles are not owners.
type OperationIdentity struct {
	ActionID    string `json:"action_id"`
	OwnerUID    string `json:"owner_uid"`
	ResourceUID string `json:"resource_uid"`
	TopologyUID string `json:"topology_uid"`
	Generation  int64  `json:"generation"`
}

func (id OperationIdentity) Validate() error {
	if id.ActionID == "" || id.OwnerUID == "" || id.ResourceUID == "" || id.TopologyUID == "" || id.Generation < 1 {
		return fmt.Errorf("incomplete operation identity")
	}
	return nil
}

type operationIdentityKey struct{}

func WithOperationIdentity(ctx context.Context, id OperationIdentity) context.Context {
	return context.WithValue(ctx, operationIdentityKey{}, id)
}
func OperationIdentityFromContext(ctx context.Context) (OperationIdentity, bool) {
	id, ok := ctx.Value(operationIdentityKey{}).(OperationIdentity)
	return id, ok
}
