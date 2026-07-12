package value

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

type Value struct {
	typ  Type
	data any
}

func Null() Value            { return Value{typ: NullType} }
func Bool(v bool) Value      { return Value{typ: BoolType, data: v} }
func String(v string) Value  { return Value{typ: StringType, data: v} }
func Number(v float64) Value { return Value{typ: NumberType, data: v} }

func (v Value) Type() Type   { return v.typ }
func (v Value) IsNull() bool { return v.typ == NullType }

func FromGo(input any) (Value, error) {
	switch value := input.(type) {
	case nil:
		return Null(), nil
	case bool:
		return Bool(value), nil
	case string:
		return String(value), nil
	case int:
		return Number(float64(value)), nil
	case int8:
		return Number(float64(value)), nil
	case int16:
		return Number(float64(value)), nil
	case int32:
		return Number(float64(value)), nil
	case int64:
		return Number(float64(value)), nil
	case float32:
		return Number(float64(value)), nil
	case float64:
		return Number(value), nil
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return Value{}, fmt.Errorf("invalid number %q: %w", value, err)
		}
		return Number(number), nil
	case []any:
		items := make([]Value, len(value))
		for i := range value {
			converted, err := FromGo(value[i])
			if err != nil {
				return Value{}, fmt.Errorf("list[%d]: %w", i, err)
			}
			items[i] = converted
		}
		return Value{typ: ListType, data: items}, nil
	case map[string]any:
		items := make(map[string]Value, len(value))
		for key, raw := range value {
			converted, err := FromGo(raw)
			if err != nil {
				return Value{}, fmt.Errorf("object.%s: %w", key, err)
			}
			items[key] = converted
		}
		return Value{typ: ObjectType, data: items}, nil
	default:
		return Value{}, fmt.Errorf("unsupported value type %T", input)
	}
}

func (v Value) GoValue() any {
	switch v.typ {
	case NullType:
		return nil
	case BoolType, StringType:
		return v.data
	case NumberType:
		number := v.data.(float64)
		if number >= math.MinInt && number <= math.MaxInt && math.Trunc(number) == number {
			return int(number)
		}
		return number
	case ListType:
		values := v.data.([]Value)
		result := make([]any, len(values))
		for i := range values {
			result[i] = values[i].GoValue()
		}
		return result
	case ObjectType:
		values := v.data.(map[string]Value)
		result := make(map[string]any, len(values))
		for key, item := range values {
			result[key] = item.GoValue()
		}
		return result
	default:
		return nil
	}
}

func (v Value) Equal(other Value) bool {
	return v.typ == other.typ && reflect.DeepEqual(v.data, other.data)
}

func (v Value) MarshalJSON() ([]byte, error) { return json.Marshal(v.GoValue()) }

func (v *Value) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	converted, err := FromGo(raw)
	if err != nil {
		return err
	}
	*v = converted
	return nil
}
