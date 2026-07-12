package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePlanFingerprintRejectsEveryChangedInput(t *testing.T) {
	base := PlanInputs{Config: []byte("resource {}"), StateLineage: "lineage-1", StateSerial: 4,
		ResourceSchemas: map[string]int{"sysbox_node": 1}, Drivers: map[string]string{"docker": "1"},
		Artifacts: map[string]string{"image": "sha256:a"}, Variables: map[string]any{"region": "local"}}
	want, err := BuildPlanFingerprint(base)
	require.NoError(t, err)
	cases := map[string]func(*PlanInputs){
		"configuration":    func(v *PlanInputs) { v.Config = []byte("changed") },
		"state lineage":    func(v *PlanInputs) { v.StateLineage = "lineage-2" },
		"state serial":     func(v *PlanInputs) { v.StateSerial++ },
		"resource schemas": func(v *PlanInputs) { v.ResourceSchemas = map[string]int{"sysbox_node": 2} },
		"drivers":          func(v *PlanInputs) { v.Drivers = map[string]string{"docker": "2"} },
		"artifacts":        func(v *PlanInputs) { v.Artifacts = map[string]string{"image": "sha256:b"} },
		"variables":        func(v *PlanInputs) { v.Variables = map[string]any{"region": "remote"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			got, err := BuildPlanFingerprint(changed)
			require.NoError(t, err)
			require.ErrorContains(t, ValidatePlanFingerprint(want, got), name)
		})
	}
}
