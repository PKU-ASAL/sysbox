package transport

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
)

type taskCancellationKey struct{}

// WithTaskCancellation supplies stop-only authority. It is deliberately
// separate from taskAuthorizationKey and can never satisfy a Start request.
func WithTaskCancellation(ctx context.Context, proof controlplane.SignedExecutionCancellation) context.Context {
	return context.WithValue(ctx, taskCancellationKey{}, proof)
}

func TaskCancellationFromContext(ctx context.Context) (controlplane.SignedExecutionCancellation, bool) {
	proof, ok := ctx.Value(taskCancellationKey{}).(controlplane.SignedExecutionCancellation)
	return proof, ok
}
