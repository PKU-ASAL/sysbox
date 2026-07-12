package secret

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Reference struct {
	Source string `json:"source"`
	Name   string `json:"name"`
}

func Environment(name string) Reference { return Reference{Source: "env", Name: name} }
func (r Reference) String() string      { return "secret://" + r.Source + "/" + url.PathEscape(r.Name) }
func Parse(input string) (Reference, error) {
	parsed, err := url.Parse(input)
	if err != nil || parsed.Scheme != "secret" || parsed.Host == "" {
		return Reference{}, fmt.Errorf("invalid secret reference %q", input)
	}
	name, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, "/"))
	if err != nil || name == "" {
		return Reference{}, fmt.Errorf("invalid secret reference %q", input)
	}
	return Reference{Source: parsed.Host, Name: name}, nil
}
func IsReference(input string) bool { return strings.HasPrefix(input, "secret://") }

type Resolver interface {
	Resolve(context.Context, Reference) (string, error)
}
type EnvironmentResolver struct{ Lookup func(string) (string, bool) }

func (r EnvironmentResolver) Resolve(_ context.Context, reference Reference) (string, error) {
	if reference.Source != "env" {
		return "", fmt.Errorf("unsupported secret source %q", reference.Source)
	}
	lookup := r.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	value, ok := lookup(reference.Name)
	if !ok {
		return "", fmt.Errorf("environment secret %s is not set", reference.Name)
	}
	return value, nil
}
func ResolveString(ctx context.Context, resolver Resolver, input string) (string, error) {
	if !IsReference(input) {
		return input, nil
	}
	reference, err := Parse(input)
	if err != nil {
		return "", err
	}
	return resolver.Resolve(ctx, reference)
}
func ResolveStringMap(ctx context.Context, resolver Resolver, input map[string]string) (map[string]string, error) {
	output := make(map[string]string, len(input))
	for key, value := range input {
		resolved, err := ResolveString(ctx, resolver, value)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", key, err)
		}
		output[key] = resolved
	}
	return output, nil
}

func ResolveAny(ctx context.Context, resolver Resolver, input any) (any, error) {
	switch value := input.(type) {
	case string:
		return ResolveString(ctx, resolver, value)
	case []any:
		output := make([]any, len(value))
		for i := range value {
			resolved, err := ResolveAny(ctx, resolver, value[i])
			if err != nil {
				return nil, err
			}
			output[i] = resolved
		}
		return output, nil
	case map[string]any:
		output := make(map[string]any, len(value))
		for key, item := range value {
			resolved, err := ResolveAny(ctx, resolver, item)
			if err != nil {
				return nil, fmt.Errorf("resolve %s: %w", key, err)
			}
			output[key] = resolved
		}
		return output, nil
	default:
		return input, nil
	}
}
