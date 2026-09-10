package adapter

// PathKeyMerger describes adapters whose shared files use different formats.
// The path is absolute, including for orphan cleanup and agent disable --purge.
// Implementations must return the same strategy Render stamps on that path.
type PathKeyMerger interface {
	KeyMergeStrategyForPath(path string) string
}

// MergeStrategyForPath resolves the adapter's format for a shared destination.
// Single-format adapters keep their existing KeyMergeStrategy contract.
func MergeStrategyForPath(a Adapter, path string) string {
	if m, ok := a.(PathKeyMerger); ok {
		return m.KeyMergeStrategyForPath(path)
	}
	return a.KeyMergeStrategy()
}
