package address

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type parser struct {
	input string
	pos   int
}

func Parse(input string) (Address, error) {
	p := &parser{input: input}
	var modules []ModuleInstance
	for p.hasPrefix("module.") {
		p.pos += len("module.")
		name, err := p.identifier("module name")
		if err != nil {
			return Address{}, err
		}
		key, err := p.instanceKey()
		if err != nil {
			return Address{}, err
		}
		if err := p.consume('.', "'.' after module instance"); err != nil {
			return Address{}, err
		}
		modules = append(modules, ModuleInstance{Name: name, Key: key})
	}
	typ, err := p.identifier("resource type")
	if err != nil {
		return Address{}, err
	}
	if strings.HasPrefix(typ, "module_") {
		return Address{}, p.errorf("canonical module path")
	}
	if err := p.consume('.', "'.' between resource type and name"); err != nil {
		return Address{}, err
	}
	name, err := p.identifier("resource name")
	if err != nil {
		return Address{}, err
	}
	key, err := p.instanceKey()
	if err != nil {
		return Address{}, err
	}
	if p.pos != len(p.input) {
		return Address{}, p.errorf("end of address")
	}
	return Address{ModulePath: modules, Type: typ, Name: name, Key: key}, nil
}

func (p *parser) identifier(expected string) (string, error) {
	start := p.pos
	r, size := p.peekRune()
	if size == 0 || !(r == '_' || unicode.IsLetter(r)) {
		return "", p.errorf(expected)
	}
	p.pos += size
	for {
		r, size = p.peekRune()
		if size == 0 || !(r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			break
		}
		p.pos += size
	}
	return p.input[start:p.pos], nil
}

func (p *parser) instanceKey() (InstanceKey, error) {
	if p.pos >= len(p.input) || p.input[p.pos] != '[' {
		return InstanceKey{}, nil
	}
	p.pos++
	if p.pos >= len(p.input) {
		return InstanceKey{}, p.errorf("instance key")
	}
	if p.input[p.pos] == '"' {
		start := p.pos
		p.pos++
		for p.pos < len(p.input) {
			switch p.input[p.pos] {
			case '\\':
				p.pos += 2
			case '"':
				p.pos++
				raw := p.input[start:p.pos]
				value, err := strconv.Unquote(raw)
				if err != nil {
					return InstanceKey{}, p.errorf("valid quoted instance key")
				}
				if err := p.consume(']', "']' after instance key"); err != nil {
					return InstanceKey{}, err
				}
				return StringKeyValue(value), nil
			default:
				_, size := utf8.DecodeRuneInString(p.input[p.pos:])
				p.pos += size
			}
		}
		return InstanceKey{}, p.errorf("closing quote")
	}
	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos {
		return InstanceKey{}, p.errorf("non-negative integer or quoted string instance key")
	}
	n, err := strconv.Atoi(p.input[start:p.pos])
	if err != nil {
		return InstanceKey{}, p.errorf("valid integer instance key")
	}
	if err := p.consume(']', "']' after instance key"); err != nil {
		return InstanceKey{}, err
	}
	return IntKeyValue(n), nil
}

func (p *parser) consume(want byte, expected string) error {
	if p.pos >= len(p.input) || p.input[p.pos] != want {
		return p.errorf(expected)
	}
	p.pos++
	return nil
}

func (p *parser) hasPrefix(prefix string) bool {
	return len(p.input)-p.pos >= len(prefix) && p.input[p.pos:p.pos+len(prefix)] == prefix
}

func (p *parser) peekRune() (rune, int) {
	if p.pos >= len(p.input) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(p.input[p.pos:])
}

func (p *parser) errorf(expected string) error {
	return fmt.Errorf("invalid resource address at byte %d: expected %s", p.pos, expected)
}
