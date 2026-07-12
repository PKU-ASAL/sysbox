package value

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValueRoundTripsSupportedTypesDeterministically(t *testing.T) {
	input := map[string]any{
		"enabled": true,
		"name":    "web",
		"count":   3,
		"ratio":   1.5,
		"tags":    []any{"blue", "edge"},
		"nested":  map[string]any{"z": "last", "a": "first"},
		"nothing": nil,
	}
	value, err := FromGo(input)
	require.NoError(t, err)
	require.Equal(t, ObjectType, value.Type())

	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, `{"count":3,"enabled":true,"name":"web","nested":{"a":"first","z":"last"},"nothing":null,"ratio":1.5,"tags":["blue","edge"]}`, string(raw))

	var decoded Value
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, input, decoded.GoValue())
}

func TestValueOwnsInputAndOutput(t *testing.T) {
	input := map[string]any{"items": []any{"one"}}
	value, err := FromGo(input)
	require.NoError(t, err)
	input["items"].([]any)[0] = "mutated"
	require.Equal(t, "one", value.GoValue().(map[string]any)["items"].([]any)[0])

	output := value.GoValue().(map[string]any)
	output["items"].([]any)[0] = "mutated again"
	require.Equal(t, "one", value.GoValue().(map[string]any)["items"].([]any)[0])
}

func TestFromGoRejectsUnsupportedValues(t *testing.T) {
	_, err := FromGo(make(chan int))
	require.ErrorContains(t, err, "unsupported value type")
}
