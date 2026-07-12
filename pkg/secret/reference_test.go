package secret

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReferenceRoundTripAndResolve(t *testing.T) {
	reference := Environment("API_TOKEN")
	require.Equal(t, "secret://env/API_TOKEN", reference.String())
	parsed, err := Parse(reference.String())
	require.NoError(t, err)
	require.Equal(t, reference, parsed)

	resolver := EnvironmentResolver{Lookup: func(name string) (string, bool) { return "canary-secret", name == "API_TOKEN" }}
	value, err := resolver.Resolve(context.Background(), parsed)
	require.NoError(t, err)
	require.Equal(t, "canary-secret", value)
}

func TestEnvironmentResolverRejectsMissingValue(t *testing.T) {
	_, err := (EnvironmentResolver{Lookup: func(string) (string, bool) { return "", false }}).Resolve(context.Background(), Environment("MISSING"))
	require.ErrorContains(t, err, "MISSING")
}

func TestResolveStringLeavesPlainValuesUnchanged(t *testing.T) {
	resolver := EnvironmentResolver{Lookup: func(string) (string, bool) { return "resolved", true }}
	value, err := ResolveString(context.Background(), resolver, "plain")
	require.NoError(t, err)
	require.Equal(t, "plain", value)
}
