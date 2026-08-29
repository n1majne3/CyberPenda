//go:build !windows

package runtime_test

import "testing"

func equivalentSessionPath(_ *testing.T, path string) string {
	return path
}
