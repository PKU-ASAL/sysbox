package value

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type StepKind uint8

const (
	AttributeStep StepKind = iota
	IndexStep
	KeyStep
)

type Step struct {
	Kind  StepKind
	Name  string
	Index int
}
type Path struct{ steps []Step }

func ParsePath(input string) (Path, error) {
	if input == "" {
		return Path{}, fmt.Errorf("empty attribute path")
	}
	var path Path
	for offset := 0; offset < len(input); {
		if len(path.steps) > 0 && input[offset] == '.' {
			offset++
		}
		if offset >= len(input) || !isIdentStart(rune(input[offset])) {
			return Path{}, fmt.Errorf("invalid attribute path at byte %d", offset)
		}
		start := offset
		offset++
		for offset < len(input) && isIdentContinue(rune(input[offset])) {
			offset++
		}
		path.steps = append(path.steps, Step{Kind: AttributeStep, Name: input[start:offset]})
		for offset < len(input) && input[offset] == '[' {
			offset++
			if offset < len(input) && input[offset] == '"' {
				start = offset
				for offset++; offset < len(input) && input[offset] != '"'; offset++ {
					if input[offset] == '\\' {
						offset++
					}
				}
				if offset >= len(input) {
					return Path{}, fmt.Errorf("unterminated key at byte %d", start)
				}
				quoted := input[start : offset+1]
				key, err := strconv.Unquote(quoted)
				if err != nil {
					return Path{}, err
				}
				offset++
				path.steps = append(path.steps, Step{Kind: KeyStep, Name: key})
			} else {
				start = offset
				for offset < len(input) && unicode.IsDigit(rune(input[offset])) {
					offset++
				}
				if start == offset {
					return Path{}, fmt.Errorf("invalid index at byte %d", start)
				}
				index, _ := strconv.Atoi(input[start:offset])
				path.steps = append(path.steps, Step{Kind: IndexStep, Index: index})
			}
			if offset >= len(input) || input[offset] != ']' {
				return Path{}, fmt.Errorf("expected ] at byte %d", offset)
			}
			offset++
		}
		if offset < len(input) && input[offset] != '.' {
			return Path{}, fmt.Errorf("unexpected character at byte %d", offset)
		}
	}
	return path, nil
}

func MustParsePath(input string) Path {
	path, err := ParsePath(input)
	if err != nil {
		panic(err)
	}
	return path
}
func (p Path) String() string {
	var out strings.Builder
	for i, step := range p.steps {
		switch step.Kind {
		case AttributeStep:
			if i > 0 {
				out.WriteByte('.')
			}
			out.WriteString(step.Name)
		case IndexStep:
			fmt.Fprintf(&out, "[%d]", step.Index)
		case KeyStep:
			out.WriteByte('[')
			out.WriteString(strconv.Quote(step.Name))
			out.WriteByte(']')
		}
	}
	return out.String()
}
func (p Path) Equal(other Path) bool { return p.String() == other.String() }
func isIdentStart(r rune) bool       { return r == '_' || unicode.IsLetter(r) }
func isIdentContinue(r rune) bool    { return isIdentStart(r) || unicode.IsDigit(r) || r == '-' }
