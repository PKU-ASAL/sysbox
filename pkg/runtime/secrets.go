package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/secret"
)

var executionSecretResolver secret.Resolver = secret.EnvironmentResolver{}

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
