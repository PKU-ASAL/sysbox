package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/oslab/sysbox/pkg/controlplane"
)

type PlanFingerprint = controlplane.PlanFingerprint

type PlanInputs struct {
	Config          []byte
	StateLineage    string
	StateSerial     int64
	ResourceSchemas map[string]int
	Drivers         map[string]string
	Artifacts       map[string]string
	Variables       map[string]any
}

func BuildPlanFingerprint(inputs PlanInputs) (PlanFingerprint, error) {
	variables, err := canonicalJSON(inputs.Variables)
	if err != nil {
		return PlanFingerprint{}, fmt.Errorf("encode plan variables: %w", err)
	}
	return PlanFingerprint{
		ConfigSHA256: sha256Hex(inputs.Config), StateLineage: inputs.StateLineage, StateSerial: inputs.StateSerial,
		ResourceSchemas: cloneStringIntMap(inputs.ResourceSchemas), Drivers: cloneStringMap(inputs.Drivers),
		Artifacts: cloneStringMap(inputs.Artifacts), VariablesSHA256: sha256Hex(variables),
	}, nil
}

func ValidatePlanFingerprint(expected, actual PlanFingerprint) error {
	checks := []struct {
		name  string
		equal bool
	}{
		{"configuration", expected.ConfigSHA256 == actual.ConfigSHA256},
		{"state lineage", expected.StateLineage == actual.StateLineage},
		{"state serial", expected.StateSerial == actual.StateSerial},
		{"resource schemas", reflect.DeepEqual(expected.ResourceSchemas, actual.ResourceSchemas)},
		{"drivers", reflect.DeepEqual(expected.Drivers, actual.Drivers)},
		{"artifacts", reflect.DeepEqual(expected.Artifacts, actual.Artifacts)},
		{"variables", expected.VariablesSHA256 == actual.VariablesSHA256},
	}
	for _, check := range checks {
		if !check.equal {
			return fmt.Errorf("stale plan: %s changed", check.name)
		}
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	// encoding/json sorts string map keys; normalize nil to an empty object.
	if value == nil {
		value = map[string]any{}
	}
	return json.Marshal(value)
}

func sha256Hex(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func cloneStringIntMap(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func SortedFingerprintKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
