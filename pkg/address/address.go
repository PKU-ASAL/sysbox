// Package address defines canonical identities for managed resources.
package address

import (
	"strconv"
	"strings"
)

type KeyKind uint8

const (
	NoKey KeyKind = iota
	IntKey
	StringKey
)

type InstanceKey struct {
	kind KeyKind
	num  int
	str  string
}

func IntKeyValue(index int) InstanceKey {
	if index < 0 {
		panic("address: negative instance index")
	}
	return InstanceKey{kind: IntKey, num: index}
}

func StringKeyValue(key string) InstanceKey {
	return InstanceKey{kind: StringKey, str: key}
}

func (k InstanceKey) IsSet() bool { return k.kind != NoKey }

func (k InstanceKey) String() string {
	switch k.kind {
	case IntKey:
		return "[" + strconv.Itoa(k.num) + "]"
	case StringKey:
		return "[" + strconv.Quote(k.str) + "]"
	default:
		return ""
	}
}

type ModuleInstance struct {
	Name string
	Key  InstanceKey
}

type Address struct {
	ModulePath []ModuleInstance
	Type       string
	Name       string
	Key        InstanceKey
}

func Resource(typ, name string) Address {
	return Address{Type: typ, Name: name}
}

func IntInstance(typ, name string, index int) Address {
	return Address{Type: typ, Name: name, Key: IntKeyValue(index)}
}

func StringInstance(typ, name, key string) Address {
	return Address{Type: typ, Name: name, Key: StringKeyValue(key)}
}

func (a Address) WithModule(module ModuleInstance) Address {
	path := make([]ModuleInstance, len(a.ModulePath), len(a.ModulePath)+1)
	copy(path, a.ModulePath)
	path = append(path, module)
	a.ModulePath = path
	return a
}

func (a Address) Clone() Address {
	if len(a.ModulePath) == 0 {
		return a
	}
	path := make([]ModuleInstance, len(a.ModulePath))
	copy(path, a.ModulePath)
	a.ModulePath = path
	return a
}

func (a Address) IsZero() bool {
	return len(a.ModulePath) == 0 && a.Type == "" && a.Name == "" && !a.Key.IsSet()
}

func (a Address) Equal(other Address) bool {
	return a.String() == other.String()
}

func (a Address) Less(other Address) bool {
	if path := compareModulePath(a.ModulePath, other.ModulePath); path != 0 {
		return path < 0
	}
	if a.Type != other.Type {
		return a.Type < other.Type
	}
	if a.Name != other.Name {
		return a.Name < other.Name
	}
	return compareKey(a.Key, other.Key) < 0
}

func (a Address) String() string {
	var value strings.Builder
	for _, module := range a.ModulePath {
		value.WriteString("module.")
		value.WriteString(module.Name)
		value.WriteString(module.Key.String())
		value.WriteByte('.')
	}
	value.WriteString(a.Type)
	value.WriteByte('.')
	value.WriteString(a.Name)
	value.WriteString(a.Key.String())
	return value.String()
}

func compareModulePath(left, right []ModuleInstance) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if left[i].Name < right[i].Name {
			return -1
		}
		if left[i].Name > right[i].Name {
			return 1
		}
		if key := compareKey(left[i].Key, right[i].Key); key != 0 {
			return key
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func compareKey(left, right InstanceKey) int {
	if left.kind < right.kind {
		return -1
	}
	if left.kind > right.kind {
		return 1
	}
	switch left.kind {
	case IntKey:
		if left.num < right.num {
			return -1
		}
		if left.num > right.num {
			return 1
		}
	case StringKey:
		if left.str < right.str {
			return -1
		}
		if left.str > right.str {
			return 1
		}
	}
	return 0
}
