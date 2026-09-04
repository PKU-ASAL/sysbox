package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/secret"
)

var executionSecretResolver secret.Resolver = secret.EnvironmentResolver{}

// SetExecutionInputs installs a resolver that, in addition to the environment,
// resolves secret://input/<key> references against the given apply-time inputs.
// It returns a restore function the caller must invoke after the run.
//
// The resolver is process-global, so concurrent applies with different inputs
// would race; applies are serialised per topology, and the inputs for a given
// topology are stable across the run, so this is safe for the common case.
func SetExecutionInputs(inputs map[string]string) (restore func()) {
	previous := executionSecretResolver
	executionSecretResolver = secret.Dispatcher{
		"env":   secret.EnvironmentResolver{},
		"input": secret.InputResolver{Inputs: inputs},
	}
	return func() { executionSecretResolver = previous }
}

func resolveSecretMap(ctx context.Context, input map[string]string) (map[string]string, error) {
	return secret.ResolveStringMap(ctx, executionSecretResolver, input)
}
func resolveSecretStrings(ctx context.Context, input []string) ([]string, error) {
	output := make([]string, len(input))
	for i, item := range input {
		resolved, err := secret.ResolveString(ctx, executionSecretResolver, item)
		if err != nil {
			return nil, fmt.Errorf("secret item %d: %w", i, err)
		}
		output[i] = resolved
	}
	return output, nil
}
func mustResolveSecretMap(ctx context.Context, input map[string]string) map[string]string {
	output, _ := resolveSecretMap(ctx, input)
	return output
}
