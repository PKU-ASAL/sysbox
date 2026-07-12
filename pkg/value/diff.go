package value

import "sort"

type Change struct {
	Path   Path
	Before Value
	After  Value
}

func Diff(before, after Value) []Change {
	var changes []Change
	diffAt(Path{}, before, after, &changes)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path.String() < changes[j].Path.String() })
	return changes
}

func diffAt(path Path, before, after Value, changes *[]Change) {
	if before.Equal(after) {
		return
	}
	if before.typ == ObjectType && after.typ == ObjectType {
		left, right := before.data.(map[string]Value), after.data.(map[string]Value)
		keys := map[string]struct{}{}
		for key := range left {
			keys[key] = struct{}{}
		}
		for key := range right {
			keys[key] = struct{}{}
		}
		for key := range keys {
			a, ok := left[key]
			if !ok {
				a = Null()
			}
			b, ok := right[key]
			if !ok {
				b = Null()
			}
			diffAt(appendStep(path, Step{Kind: AttributeStep, Name: key}), a, b, changes)
		}
		return
	}
	if before.typ == ListType && after.typ == ListType {
		left, right := before.data.([]Value), after.data.([]Value)
		length := len(left)
		if len(right) > length {
			length = len(right)
		}
		for i := 0; i < length; i++ {
			a, b := Null(), Null()
			if i < len(left) {
				a = left[i]
			}
			if i < len(right) {
				b = right[i]
			}
			diffAt(appendStep(path, Step{Kind: IndexStep, Index: i}), a, b, changes)
		}
		return
	}
	*changes = append(*changes, Change{Path: path, Before: before, After: after})
}

func appendStep(path Path, step Step) Path {
	steps := make([]Step, len(path.steps)+1)
	copy(steps, path.steps)
	steps[len(path.steps)] = step
	return Path{steps: steps}
}
