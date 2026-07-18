package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oslab/sysbox/pkg/diag"
	"github.com/oslab/sysbox/pkg/value"
)

type AttributeBehavior string

const (
	AttributeImmutable AttributeBehavior = "immutable"
	AttributeComputed  AttributeBehavior = "computed"
)

type PersistencePolicy string

const (
	PersistPublic    PersistencePolicy = "public"
	PersistReference PersistencePolicy = "reference"
	PersistNone      PersistencePolicy = "none"
)

type AttributeSchema struct {
	Name                                    string
	Type                                    value.Type
	Required, Optional, Computed, Sensitive bool
	Behavior                                AttributeBehavior
	Default                                 value.Value
	Persistence                             PersistencePolicy
}

func (a AttributeSchema) ValidateDefinition() error {
	if a.Name == "" {
		return fmt.Errorf("attribute name is required")
	}
	if a.Required && a.Optional {
		return fmt.Errorf("attribute %s cannot be required and optional", a.Name)
	}
	if a.Required && a.Computed {
		return fmt.Errorf("attribute %s cannot be required and computed", a.Name)
	}
	if a.Type == "" {
		return fmt.Errorf("attribute %s type is required", a.Name)
	}
	if !a.Default.IsNull() && !a.Optional {
		return fmt.Errorf("attribute %s default requires optional", a.Name)
	}
	if !a.Default.IsNull() && a.Default.Type() != a.Type {
		return fmt.Errorf("attribute %s default must be %s", a.Name, a.Type)
	}
	return nil
}

type ResourceSchema struct {
	Type          string
	Version       int
	Attributes    map[string]AttributeSchema
	IgnoreChanges map[string]bool
}

func (s ResourceSchema) Attribute(name string) AttributeSchema {
	if attr, ok := s.Attributes[name]; ok {
		return attr
	}
	return AttributeSchema{Name: name, Type: value.NullType, Behavior: AttributeImmutable, Persistence: PersistNone}
}

func (s ResourceSchema) Validate(input value.Value) (value.Value, diag.Diagnostics) {
	items, ok := input.Object()
	if !ok {
		return value.Value{}, diag.Diagnostics{{Severity: diag.Error, Summary: "Invalid resource value", Detail: "resource value must be object"}}
	}
	var diagnostics diag.Diagnostics
	for name, schema := range s.Attributes {
		if err := schema.ValidateDefinition(); err != nil {
			diagnostics = append(diagnostics, diag.Diagnostic{Severity: diag.Error, Summary: "Invalid schema", Detail: err.Error()})
			continue
		}
		item, exists := items[name]
		if !exists {
			if schema.Required {
				diagnostics = append(diagnostics, diag.Diagnostic{Severity: diag.Error, Summary: "Missing required attribute", Detail: name + " is required"})
			}
			if !schema.Default.IsNull() {
				items[name] = schema.Default
			}
			continue
		}
		if item.Type() != schema.Type {
			diagnostics = append(diagnostics, diag.Diagnostic{Severity: diag.Error, Summary: "Invalid attribute type", Detail: fmt.Sprintf("%s must be %s, got %s", name, schema.Type, item.Type())})
		}
	}
	for name := range items {
		if _, exists := s.Attributes[name]; !exists {
			diagnostics = append(diagnostics, diag.Diagnostic{Severity: diag.Error, Summary: "Unknown attribute", Detail: name + " is not declared by schema"})
		}
	}
	validated, err := value.FromGo(valuesToGo(items))
	if err != nil {
		diagnostics = append(diagnostics, diag.Diagnostic{Severity: diag.Error, Summary: "Invalid resource value", Detail: err.Error()})
	}
	diagnostics.Sort()
	return validated, diagnostics
}

type TypedFieldChange struct {
	Path                                 value.Path
	Before, After                        any
	RequiresReplace, Sensitive, Computed bool
}

const sensitivePlaceholder = "(sensitive)"

func (s ResourceSchema) Diff(before, after value.Value) []TypedFieldChange {
	raw := value.Diff(before, after)
	changes := make([]TypedFieldChange, 0, len(raw))
	for _, change := range raw {
		root := strings.SplitN(strings.SplitN(change.Path.String(), "[", 2)[0], ".", 2)[0]
		attribute := s.Attribute(root)
		if s.IgnoreChanges[change.Path.String()] || s.IgnoreChanges[root] {
			continue
		}
		beforeValue, afterValue := change.Before.GoValue(), change.After.GoValue()
		if attribute.Sensitive {
			beforeValue, afterValue = sensitivePlaceholder, sensitivePlaceholder
		}
		changes = append(changes, TypedFieldChange{Path: change.Path, Before: beforeValue, After: afterValue, RequiresReplace: attribute.Behavior == AttributeImmutable, Sensitive: attribute.Sensitive, Computed: attribute.Computed || attribute.Behavior == AttributeComputed})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path.String() < changes[j].Path.String() })
	return changes
}

func valuesToGo(items map[string]value.Value) map[string]any {
	result := make(map[string]any, len(items))
	for key, item := range items {
		result[key] = item.GoValue()
	}
	return result
}

func ResourceSchemaFor(typ string) ResourceSchema {
	schema := ResourceSchema{Type: typ, Version: 1, Attributes: map[string]AttributeSchema{}, IgnoreChanges: map[string]bool{"name": true, "type": true}}
	add := func(name string, typ value.Type, computed, sensitive bool) {
		schema.Attributes[name] = AttributeSchema{Name: name, Type: typ, Optional: !computed, Computed: computed, Sensitive: sensitive, Behavior: AttributeImmutable, Persistence: PersistPublic}
	}
	switch typ {
	case "sysbox_network":
		add("cidr", value.StringType, false, false)
		add("network_type", value.StringType, false, false)
		add("nat", value.BoolType, false, false)
	case "sysbox_image":
		for _, name := range []string{"substrate", "kind", "source", "sha256", "architecture", "guest_family"} {
			add(name, value.StringType, false, false)
		}
		add("size", value.NumberType, false, false)
	case "sysbox_kernel":
		for _, name := range []string{"substrate", "source", "sha256", "architecture", "cmdline_template"} {
			add(name, value.StringType, false, false)
		}
		add("depends_on", value.ListType, false, false)
	case "sysbox_node":
		schema.Version = 2
		for _, name := range []string{"image", "substrate", "guest_family"} {
			add(name, value.StringType, false, false)
		}
		for _, name := range []string{"vcpus", "memory"} {
			add(name, value.NumberType, false, false)
		}
		for _, name := range []string{"depends_on", "links", "ports", "routes", "connections", "provisioners"} {
			add(name, value.ListType, false, name == "connections" || name == "provisioners")
		}
		for _, name := range []string{"env", "provider_config"} {
			add(name, value.ObjectType, false, true)
		}
	case "sysbox_router":
		for _, name := range []string{"substrate", "image", "nat_from", "nat_to"} {
			add(name, value.StringType, false, false)
		}
		add("interfaces", value.ListType, false, false)
	case "sysbox_firewall":
		for _, name := range []string{"attach_to", "family", "default_input", "default_output", "default_forward"} {
			add(name, value.StringType, false, false)
		}
		add("rules", value.ListType, false, false)
	case "sysbox_ssh_access":
		add("node", value.StringType, false, false)
		add("authorized_keys", value.ListType, false, true)
		add("bind_ip", value.StringType, false, false)
		add("port", value.NumberType, false, false)
	default:
		add("data", value.ObjectType, false, true)
	}
	return schema
}
