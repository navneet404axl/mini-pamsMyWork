//go:build !linux

package main

import "errors"

// evict is Linux-only; this stub lets the project build on macOS for reading.
func evict(path string) error {
	return errors.New("-evict is only supported on Linux")
}
