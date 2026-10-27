package runtime

import (
	"context"
	"fmt"
)

// Apply executes only the authorized plan. Observation can remove unnecessary
// effects, but cannot add replacement, destruction, or a new target.
func (e *Executor) Apply(ctx context.Context, plan *Plan) error {
	for _, resource := range e.state.Resources {
		if resource.RecoveryRequired() {
			return fmt.Errorf("%w: %s requires explicit reset or destroy", ErrActionUncertain, resource.Address)
		}
	}
	if err := e.preflightManagedBridges(ctx); err != nil {
		return err
	}
	operation := e.operation
	if operation == "" {
		operation = "apply"
	}
	return e.converge(ctx, plan, operation)
}
