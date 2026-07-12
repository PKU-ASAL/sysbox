package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/value"
)

func TestResourceSchemaValidatesRequiredTypesAndUnknownAttributes(t *testing.T) {
	schema := ResourceSchema{Type: "example", Version: 1, Attributes: map[string]AttributeSchema{
		"name": {Name: "name", Type: value.StringType, Required: true, Behavior: AttributeImmutable, Persistence: PersistPublic},
		"size": {Name: "size", Type: value.NumberType, Optional: true, Default: value.Number(1), Behavior: AttributeImmutable, Persistence: PersistPublic},
	}}

	_, diagnostics := schema.Validate(mustValue(t, map[string]any{"size": "large", "extra": true}))
	require.ErrorContains(t, diagnostics.Err(), "name is required")
	require.ErrorContains(t, diagnostics.Err(), "size must be number")
	require.ErrorContains(t, diagnostics.Err(), "extra is not declared")
}

func TestResourceSchemaAppliesDefaultsAndRejectsInvalidFlags(t *testing.T) {
	schema := ResourceSchema{Type: "example", Version: 1, Attributes: map[string]AttributeSchema{
		"size": {Name: "size", Type: value.NumberType, Optional: true, Default: value.Number(2), Behavior: AttributeImmutable, Persistence: PersistPublic},
	}}
	validated, diagnostics := schema.Validate(mustValue(t, map[string]any{}))
	require.NoError(t, diagnostics.Err())
	require.Equal(t, 2, validated.GoValue().(map[string]any)["size"])

	invalid := AttributeSchema{Name: "bad", Type: value.StringType, Required: true, Optional: true}
	require.ErrorContains(t, invalid.ValidateDefinition(), "required and optional")
}

func TestResourceSchemaDiffUsesOrderedNestedPathsAndRedactsSecrets(t *testing.T) {
	schema := ResourceSchema{Type: "example", Version: 1, Attributes: map[string]AttributeSchema{
		"interfaces": {Name: "interfaces", Type: value.ListType, Optional: true, Behavior: AttributeImmutable, Persistence: PersistPublic},
		"token":      {Name: "token", Type: value.StringType, Optional: true, Sensitive: true, Behavior: AttributeImmutable, Persistence: PersistReference},
		"status":     {Name: "status", Type: value.StringType, Computed: true, Behavior: AttributeComputed, Persistence: PersistPublic},
	}}
	before := mustValue(t, map[string]any{"interfaces": []any{map[string]any{"ip": "10.0.0.1"}}, "token": "secret-a", "status": "old"})
	after := mustValue(t, map[string]any{"interfaces": []any{map[string]any{"ip": "10.0.0.2"}}, "token": "secret-b", "status": "new"})

	changes := schema.Diff(before, after)
	require.Equal(t, []TypedFieldChange{
		{Path: value.MustParsePath("interfaces[0].ip"), Before: "10.0.0.1", After: "10.0.0.2", RequiresReplace: true},
		{Path: value.MustParsePath("status"), Before: "old", After: "new", Computed: true},
		{Path: value.MustParsePath("token"), Before: sensitivePlaceholder, After: sensitivePlaceholder, RequiresReplace: true, Sensitive: true},
	}, changes)
}

func mustValue(t *testing.T, input any) value.Value {
	t.Helper()
	result, err := value.FromGo(input)
	require.NoError(t, err)
	return result
}
