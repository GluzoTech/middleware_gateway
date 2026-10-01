// Package dotenv loads a KEY=value environment file into the process
// environment, so that local runs need no long `export` preamble.
//
// It is deliberately separate from package config. Configuration still comes
// from the environment and nothing else; a .env file is only one way of
// putting it there, used while developing. Variables already present in the
// environment always win, so an explicit export, a container's environment or
// an injected secret keeps its value and a stale file cannot override
// production.
//
// Parse errors carry a line number and never the line's text: an environment
// file is mostly secrets, and this error is printed to stderr.
package dotenv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// DefaultPath is the file Load reads when it is given no path.
const DefaultPath = ".env"

// PathVariable names the environment variable that overrides which file Load
// reads. It is read from the real environment, not from the file.
const PathVariable = "ENV_FILE"

// Variable is one KEY=value pair read from an environment file.
type Variable struct {
	Key   string
	Value string
}

// Load reads the environment file at path and sets every variable it defines
// that is not already present in the process environment. An empty path means
// the value of ENV_FILE, or DefaultPath when that is unset too.
//
// It returns the path it loaded, or "" when there was no file to load. A
// missing file is not an error: deployments inject the environment directly
// and ship no .env.
func Load(path string) (string, error) {
	if path == "" {
		path = os.Getenv(PathVariable)
	}
	if path == "" {
		path = DefaultPath
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("dotenv: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	vars, err := Parse(f)
	if err != nil {
		return "", fmt.Errorf("dotenv: %s: %w", path, err)
	}

	// Snapshot the pre-existing keys before setting anything, so that a key
	// repeated in the file takes its last value rather than being skipped as
	// though the environment had supplied it.
	preset := make(map[string]bool)
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok {
			preset[k] = true
		}
	}

	for _, v := range vars {
		if preset[v.Key] {
			continue
		}
		if err := os.Setenv(v.Key, v.Value); err != nil {
			return "", fmt.Errorf("dotenv: set %s: %w", v.Key, err)
		}
	}
	return path, nil
}

// Parse reads environment-file syntax from r and returns the variables in the
// order they appear.
//
// Recognised syntax:
//
//	# a comment line
//	KEY=value
//	KEY=value        # a trailing comment, stripped when a space precedes the #
//	export KEY=value
//	KEY="quoted, keeps # and spaces, understands \n \r \t \\ \""
//	KEY='quoted, taken literally'
//
// A '#' with no space before it is part of the value, so URLs and passwords
// containing one survive. A value that genuinely contains " #" must be
// quoted, or it is cut at the '#'.
func Parse(r io.Reader) ([]Variable, error) {
	var vars []Variable

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for line := 1; sc.Scan(); line++ {
		// The BOM only ever appears on the first line; trimming it on every
		// line is harmless and keeps the loop free of a special case.
		text := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(text, "export "); ok {
			text = strings.TrimSpace(rest)
		}

		key, rest, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: missing '='", line)
		}
		key = strings.TrimSpace(key)
		if !validKey(key) {
			return nil, fmt.Errorf("line %d: invalid variable name", line)
		}

		value, err := parseValue(strings.TrimLeft(rest, " \t"))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		vars = append(vars, Variable{Key: key, Value: value})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return vars, nil
}

// parseValue interprets the text to the right of the '=', with leading blanks
// already removed.
func parseValue(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single quote")
		}
		return s[1 : 1+end], nil
	case '"':
		return unquote(s)
	}
	if i := commentIndex(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t"), nil
}

// unquote reads a double-quoted value starting at s[0], expanding the escapes
// a shell-style environment file is expected to understand. Anything after
// the closing quote is a comment and is discarded.
func unquote(s string) (string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if i+1 >= len(s) {
				return "", errors.New("unterminated double quote")
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '\\', '"':
				b.WriteByte(s[i])
			default:
				// An unknown escape is not an error: keep both bytes, so a
				// Windows path or a regex in a value survives unharmed.
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		case '"':
			return b.String(), nil
		default:
			b.WriteByte(s[i])
		}
	}
	return "", errors.New("unterminated double quote")
}

// commentIndex reports where an unquoted value's trailing comment starts, or
// -1. Only a '#' at the start or preceded by a blank opens a comment.
func commentIndex(s string) int {
	for i := range len(s) {
		if s[i] != '#' {
			continue
		}
		if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
			return i
		}
	}
	return -1
}

// validKey reports whether k is a usable environment variable name.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i := range len(k) {
		c := k[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
