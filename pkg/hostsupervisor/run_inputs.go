package hostsupervisor

import (
	"context"
	"maps"
)

type runInputsKey struct{}

// Inputs travel separately from RunRequest so admission/checkpoints never
// serialize them. The trusted host validates their durable request fingerprint.
func WithRunInputs(ctx context.Context, inputs map[string]string) context.Context {
	return context.WithValue(ctx, runInputsKey{}, maps.Clone(inputs))
}
func RunInputs(ctx context.Context) map[string]string {
	inputs, _ := ctx.Value(runInputsKey{}).(map[string]string)
	return maps.Clone(inputs)
}
