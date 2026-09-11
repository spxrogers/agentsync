package marketplace

import "strings"

// CacheAsideSuffix names the sibling a cache directory is parked at while it is
// being replaced: the cli's swapDir renames the old tree to <dir>..old, moves
// the new tree in, and discards the aside only then — so a replace that fails
// can put the old tree back, and one that is interrupted leaves it there until
// the next replace of the same directory clears it. The name holds "..", which
// the cli's cache-key sanitizer never lets into a cache directory name, so an
// aside can never be a cache of its own.
const CacheAsideSuffix = "..old"

// IsCacheAside reports whether a cache-root entry is the parked old tree of a
// replace in progress (or interrupted) rather than a cache: its name holds
// "..", which no cache directory name can. Every scan of a cache root skips
// such an entry — offered as a marketplace of its own, a stale copy would be
// resolvable under a name no cache directory can be derived from.
func IsCacheAside(name string) bool { return strings.Contains(name, "..") }
