// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package compose

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
			for j < len(s) && isNameChar(s[j]) {
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
	for j < len(body) && isNameChar(body[j]) {
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

func isNameChar(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}

// parseEnvFile reads KEY=VALUE lines, as a .env or env_file holds them. Blank
// lines and lines starting with # are skipped, an export before the key is
// allowed, and a value in matching quotes has them removed. A line with only
// a KEY is recorded with no value.
func parseEnvFile(r io.Reader) (map[string]*string, error) {
	env := make(map[string]*string)

	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")

		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("line %d: want KEY=VALUE, not %q", line, text)
		}
		if !ok {
			env[key] = nil
			continue
		}

		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env[key] = &value
	}

	return env, scanner.Err()
}

// splitShellWords splits a command line into arguments as a shell would,
// honouring single and double quotes and backslash escapes. It expands
// nothing.
func splitShellWords(s string) ([]string, error) {
	var (
		args    []string
		word    strings.Builder
		inWord  bool
		quote   byte
		escaped bool
	)

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			word.WriteByte(c)
			escaped = false
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case quote == '"':
			switch {
			case c == '"':
				quote = 0
			case c == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`+"`", s[i+1]) >= 0:
				word.WriteByte(s[i+1])
				i++
			default:
				word.WriteByte(c)
			}
		case c == '\\':
			escaped, inWord = true, true
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				args = append(args, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteByte(c)
			inWord = true
		}
	}

	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, s)
	}
	if escaped {
		return nil, errors.New("a command cannot end in a backslash")
	}
	if inWord {
		args = append(args, word.String())
	}
	return args, nil
}
