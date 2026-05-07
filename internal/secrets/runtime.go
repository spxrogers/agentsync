package secrets

import "runtime"

// runtimeIsWindows is split out so tests can override OS-specific behavior
// without runtime.GOOS string comparisons sprayed across the package.
func runtimeIsWindows() bool { return runtime.GOOS == "windows" }
