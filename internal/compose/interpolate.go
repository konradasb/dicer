// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"fmt"
	"strings"
)

// Lookup finds a variable's value, and whether it is set.
type Lookup func(name string) (string, bool)

// interpolate replaces the variables in s as Docker Compose does:
//
//	$VAR, ${VAR}        the value, or empty if unset
//	${VAR:-default}     default if VAR is unset or empty
//	${VAR-default}      default if VAR is unset
//	${VAR:?message}     fails with message if VAR is unset or empty
//	${VAR?message}      fails with message if VAR is unset
//	${VAR:+other}       other if VAR is set and not empty, otherwise empty
//	${VAR+other}        other if VAR is set, otherwise empty
//	$$                  a literal $
//
// A default, message or other may itself hold variables.
func interpolate(s string, lookup Lookup) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			out.WriteByte(s[i])
			continue
		}
		if i+1 == len(s) {
			out.WriteByte('$')
			continue
		}

		switch next := s[i+1]; {
		case next == '$':
			out.WriteByte('$')
			i++
		case next == '{':
			end := closingBrace(s, i+2)
			if end < 0 {
				return "", fmt.Errorf("invalid interpolation %q: no closing }", s[i:])
			}
			value, err := expand(s[i+2:end], lookup)
			if err != nil {
				return "", err
			}
			out.WriteString(value)
			i = end
		case isNameStart(next):
			j := i + 1
			for j < len(s) && isNameCharacter(s[j]) {
				j++
			}
			value, _ := lookup(s[i+1 : j])
			out.WriteString(value)
			i = j - 1
		default:
			out.WriteByte('$')
		}
	}
	return out.String(), nil
}

// closingBrace finds the } that closes the ${ whose body starts at from,
// passing over the ${...} nested in it.
func closingBrace(s string, from int) int {
	depth := 1
	for i := from; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// expand evaluates the body of a ${...}.
func expand(body string, lookup Lookup) (string, error) {
	j := 0
	for j < len(body) && isNameCharacter(body[j]) {
		j++
	}
	name, rest := body[:j], body[j:]
	if name == "" || !isNameStart(name[0]) {
		return "", fmt.Errorf("invalid interpolation ${%s}: want a variable name", body)
	}

	value, set := lookup(name)
	if rest == "" {
		return value, nil
	}

	emptyCounts := strings.HasPrefix(rest, ":")
	op := strings.TrimPrefix(rest, ":")
	if op == "" {
		return "", fmt.Errorf("invalid interpolation ${%s}", body)
	}
	arg := op[1:]
	present := set && (!emptyCounts || value != "")

	switch op[0] {
	case '-':
		if present {
			return value, nil
		}
		return interpolate(arg, lookup)
	case '?':
		if present {
			return value, nil
		}
		msg, err := interpolate(arg, lookup)
		if err != nil {
			return "", err
		}
		if msg == "" {
			msg = "it is not set"
		}
		return "", fmt.Errorf("required variable %s: %s", name, msg)
	case '+':
		if present {
			return interpolate(arg, lookup)
		}
		return "", nil
	default:
		return "", fmt.Errorf("invalid interpolation ${%s}: want :-, -, :?, ?, :+ or + after the name", body)
	}
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameCharacter(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}
