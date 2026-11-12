// Package config reads settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// List returns key split on commas, with spaces and empty items removed,
// or def when it is unset. Example: "http://localhost:5173, http://localhost:5174".
func List(key string, def []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// String returns the value of key, or def when it is unset or empty.
func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Int returns key as an int, or def when it is unset. A value that is not a number is an error.
func Int(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a whole number, got %q", key, v)
	}
	return n, nil
}

// Bool returns key as a bool, or def when it is unset or empty. It accepts what strconv.ParseBool
// accepts (true, false, 1, 0, t, f, in any case); anything else is an error, so a typo such as
// "ture" never silently switches a feature off.
func Bool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s must be true or false, got %q", key, v)
	}
	return b, nil
}

// Duration returns key parsed with time.ParseDuration (for example "5s"), or def when it is unset.
func Duration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration like 5s or 1m, got %q", key, v)
	}
	return d, nil
}
