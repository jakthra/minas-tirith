// The .env file: local development configuration. Both binaries load
// the working directory's .env at startup, before resolving flags,
// shell environment variables, and defaults, so local values live in
// one file instead of every shell session. The committed .env.example
// next to go.mod documents every supported variable; .env itself is
// gitignored and holds the local values and secrets.

package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DotEnvFile is the name of the development configuration file both
// binaries load at startup: ".env" in the working directory. The
// committed .env.example next to go.mod documents it; .env itself is
// gitignored and holds the local values.
const DotEnvFile = ".env"

// loadDotEnv reads the working directory's .env file and returns its
// KEY=VALUE entries, or nil when the file does not exist: a missing
// .env is development as usual, not an error. A malformed file is an
// error naming the offending line, so a typo fails startup instead of
// silently dropping a setting.
func loadDotEnv() (map[string]string, error) {
	data, err := os.ReadFile(DotEnvFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseDotEnv(data)
}

// parseDotEnv parses .env content: one KEY=VALUE entry per line, blank
// lines and # comments skipped. Keys may carry an `export ` prefix so
// the file can be sourced by a shell too; values may be single- or
// double-quoted, and unquoted values may carry a trailing `# comment`.
func parseDotEnv(data []byte) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, err := parseDotEnvEntry(text)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", DotEnvFile, line, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", DotEnvFile, err)
	}
	return values, nil
}

// parseDotEnvEntry parses one non-blank, non-comment .env line into
// its key and value.
func parseDotEnvEntry(entry string) (key, value string, err error) {
	entry = strings.TrimPrefix(entry, "export ")
	name, val, found := strings.Cut(entry, "=")
	if !found {
		return "", "", fmt.Errorf("expected KEY=VALUE, got %q", entry)
	}
	key = strings.TrimSpace(name)
	if key == "" {
		return "", "", fmt.Errorf("expected KEY=VALUE, got %q", entry)
	}
	if strings.ContainsAny(key, " \t") {
		return "", "", fmt.Errorf("key %q contains whitespace", key)
	}
	value, err = parseDotEnvValue(val)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", key, err)
	}
	return key, value, nil
}

// parseDotEnvValue parses the raw text after `=`. A quoted value keeps
// everything between its quotes: escapes are not processed, and the
// closing quote must end the line. An unquoted value ends at the first
// `#` preceded by whitespace, so `KEY=value # comment` sets KEY to
// `value`; a `#` without preceding whitespace is part of the value,
// like in a shell.
func parseDotEnvValue(val string) (string, error) {
	if trimmed := strings.TrimSpace(val); len(trimmed) > 0 && (trimmed[0] == '"' || trimmed[0] == '\'') {
		quote := string(trimmed[0])
		if inner, found := strings.CutSuffix(trimmed[1:], quote); found {
			return inner, nil
		}
		if trimmed[0] == '"' {
			return "", errors.New("unterminated double-quoted value")
		}
		return "", errors.New("unterminated single-quoted value")
	}
	for i := 1; i < len(val); i++ {
		if val[i] == '#' && (val[i-1] == ' ' || val[i-1] == '\t') {
			return strings.TrimSpace(val[:i]), nil
		}
	}
	return strings.TrimSpace(val), nil
}

// envLookup returns the environment lookup both binaries resolve with:
// the shell environment first, then the .env file's values. The file
// fills gaps — a variable already set to a non-empty value in the
// shell wins — so exported values keep working unchanged.
// Command-line flags layer over both wherever they are applied.
func envLookup(dotenv map[string]string) func(string) string {
	return func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return dotenv[key]
	}
}
