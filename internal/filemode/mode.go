// Package filemode compares the permission bits supported by the host OS.
package filemode

import (
	"os"
	"runtime"
)

// Equal reports whether chmod can distinguish actual from desired permissions.
// Windows chmod only honors the owner-write bit (the read-only attribute).
// Unix compares all nine permission bits, including execute and group/other bits.
func Equal(actual, desired os.FileMode) bool {
	return equalForOS(actual, desired, runtime.GOOS)
}

func equalForOS(actual, desired os.FileMode, goos string) bool {
	if goos == "windows" {
		return actual&0o200 == desired&0o200
	}
	return actual.Perm() == desired.Perm()
}
