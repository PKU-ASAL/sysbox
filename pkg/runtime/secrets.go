package runtime

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/secret"
)

// resolveSecretMap resolves secret references in a string map using the
// executor's resolver.
func (e *Executor) resolveSecretMap(ctx context.Context, input map[string]string) (map[string]string, error) {
	return secret.ResolveStringMap(ctx, e.resolver(), input)
}

// resolveSecretStrings resolves secret references in a string slice using the
// executor's resolver.
func (e *Executor) resolveSecretStrings(ctx context.Context, input []string) ([]string, error) {
	output := make([]string, len(input))
	for i, item := range input {
		resolved, err := secret.ResolveString(ctx, e.resolver(), item)
		if err != nil {
			return nil, fmt.Errorf("secret item %d: %w", i, err)
		}
		output[i] = resolved
	}
	return output, nil
}
