package runtime

import "context"

func (e *Executor) Destroy(ctx context.Context, plan *Plan) error {
	return e.converge(ctx, plan, "destroy")
}
