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

func TestResolveAnyPreservesTypedPointerAndResolvesFields(t *testing.T) {
	type nested struct {
		Token string
	}
	type config struct {
		Name   string
		Nested nested
		Values []string
	}
	input := &config{Name: "plain", Nested: nested{Token: Environment("TOKEN").String()}, Values: []string{Environment("VALUE").String()}}
	resolver := EnvironmentResolver{Lookup: func(name string) (string, bool) { return "resolved-" + name, true }}

	resolved, err := ResolveAny(context.Background(), resolver, input)
	require.NoError(t, err)
	got, ok := resolved.(*config)
	require.True(t, ok)
	require.NotSame(t, input, got)
	require.Equal(t, "plain", got.Name)
	require.Equal(t, "resolved-TOKEN", got.Nested.Token)
	require.Equal(t, []string{"resolved-VALUE"}, got.Values)
	require.Equal(t, Environment("TOKEN").String(), input.Nested.Token)
}

func TestInputReferenceRoundTrip(t *testing.T) {
	reference := Input("flag")
	require.Equal(t, "secret://input/flag", reference.String())
	parsed, err := Parse(reference.String())
	require.NoError(t, err)
	require.Equal(t, reference, parsed)
}

func TestInputResolverResolvesProvidedValue(t *testing.T) {
	resolver := InputResolver{Inputs: map[string]string{"flag": "the-secret-value"}}

	value, err := resolver.Resolve(context.Background(), Input("flag"))

	require.NoError(t, err)
	require.Equal(t, "the-secret-value", value)
}

func TestInputResolverRejectsMissingInput(t *testing.T) {
	_, err := (InputResolver{Inputs: map[string]string{}}).Resolve(context.Background(), Input("flag"))

	require.ErrorContains(t, err, "flag")
}

func TestInputResolverRejectsNonInputSource(t *testing.T) {
	_, err := (InputResolver{Inputs: map[string]string{}}).Resolve(context.Background(), Environment("X"))

	require.ErrorContains(t, err, "env")
}

func TestDispatcherRoutesBySource(t *testing.T) {
	d := Dispatcher{
		"env":   EnvironmentResolver{Lookup: func(name string) (string, bool) { return "env-" + name, true }},
		"input": InputResolver{Inputs: map[string]string{"flag": "input-flag"}},
	}

	envVal, err := d.Resolve(context.Background(), Environment("TOKEN"))
	require.NoError(t, err)
	require.Equal(t, "env-TOKEN", envVal)

	inputVal, err := d.Resolve(context.Background(), Input("flag"))
	require.NoError(t, err)
	require.Equal(t, "input-flag", inputVal)
}

func TestDispatcherRejectsUnknownSource(t *testing.T) {
	_, err := (Dispatcher{}).Resolve(context.Background(), Reference{Source: "nope", Name: "x"})
	require.ErrorContains(t, err, "nope")
}
