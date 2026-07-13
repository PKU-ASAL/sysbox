package secret

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
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
	if input == nil {
		return nil, nil
	}
	resolved, err := resolveValue(ctx, resolver, reflect.ValueOf(input))
	if err != nil {
		return nil, err
	}
	return resolved.Interface(), nil
}

func resolveValue(ctx context.Context, resolver Resolver, input reflect.Value) (reflect.Value, error) {
	if !input.IsValid() {
		return input, nil
	}
	switch input.Kind() {
	case reflect.String:
		resolved, err := ResolveString(ctx, resolver, input.String())
		if err != nil {
			return reflect.Value{}, err
		}
		output := reflect.New(input.Type()).Elem()
		output.SetString(resolved)
		return output, nil
	case reflect.Pointer:
		if input.IsNil() {
			return reflect.Zero(input.Type()), nil
		}
		resolved, err := resolveValue(ctx, resolver, input.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		output := reflect.New(input.Type().Elem())
		output.Elem().Set(resolved)
		return output, nil
	case reflect.Interface:
		if input.IsNil() {
			return reflect.Zero(input.Type()), nil
		}
		resolved, err := resolveValue(ctx, resolver, input.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		output := reflect.New(input.Type()).Elem()
		output.Set(resolved)
		return output, nil
	case reflect.Struct:
		output := reflect.New(input.Type()).Elem()
		output.Set(input)
		for i := 0; i < input.NumField(); i++ {
			if !output.Field(i).CanSet() || input.Type().Field(i).PkgPath != "" {
				continue
			}
			resolved, err := resolveValue(ctx, resolver, input.Field(i))
			if err != nil {
				return reflect.Value{}, fmt.Errorf("resolve %s: %w", input.Type().Field(i).Name, err)
			}
			output.Field(i).Set(resolved)
		}
		return output, nil
	case reflect.Slice:
		if input.IsNil() {
			return reflect.Zero(input.Type()), nil
		}
		output := reflect.MakeSlice(input.Type(), input.Len(), input.Len())
		for i := 0; i < input.Len(); i++ {
			resolved, err := resolveValue(ctx, resolver, input.Index(i))
			if err != nil {
				return reflect.Value{}, fmt.Errorf("resolve item %d: %w", i, err)
			}
			output.Index(i).Set(resolved)
		}
		return output, nil
	case reflect.Map:
		if input.IsNil() {
			return reflect.Zero(input.Type()), nil
		}
		output := reflect.MakeMapWithSize(input.Type(), input.Len())
		iterator := input.MapRange()
		for iterator.Next() {
			resolved, err := resolveValue(ctx, resolver, iterator.Value())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("resolve %v: %w", iterator.Key(), err)
			}
			output.SetMapIndex(iterator.Key(), resolved)
		}
		return output, nil
	default:
		return input, nil
	}
}
