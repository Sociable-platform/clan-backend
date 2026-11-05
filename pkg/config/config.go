// Package config reads settings from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

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
