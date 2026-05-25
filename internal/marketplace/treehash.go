package marketplace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
)

// treeHashPrefix tags a manifest SHA computed over the whole plugin cache tree
// (every projected component body, not just plugin.json). The version segment
// lets the formula evolve without silently mis-verifying old pins.
const treeHashPrefix = "tree:v1:"

// entryHashPrefix tags the pin of an entry-only plugin (one with no cached
// plugin.json / component files — its definition lives solely in the
// marketplace entry). Such a plugin ships no bodies, so there is nothing in the
// cache dir to tree-hash; verify skips it (the entry isn't available at verify
// time), matching the pre-tree-hash "no plugin.json → nothing to verify".
const entryHashPrefix = treeHashPrefix + "entry:"

// PluginTreeHash computes a deterministic content hash over every file in a
// plugin's cache dir, EXCLUDING .git/ (which projection never reads and which
// would otherwise make a git-sourced plugin's pin non-deterministic across
// clones). It covers the component bodies projection actually ships —
// convention-discovered skills, command/subagent markdown — not just
// plugin.json, so a body tamper with an unchanged plugin.json is detected.
//
// The hash is sha256 over the sorted "<slash-relpath>\x00<sha256(content)>"
// lines, so it captures file additions/removals as well as edits, and is
// independent of walk order and OS path separators. A symlink is REFUSED (not
// skipped): skipping one would let a swapped link target hide from the hash.
func PluginTreeHash(fs afero.Fs, cacheDir string) (string, error) {
	var lines []string
	err := afero.Walk(fs, cacheDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		rel, rerr := filepath.Rel(cacheDir, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin cache contains a symlink %q; refusing to hash", rel)
		}
		data, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return fmt.Errorf("hash %s: %w", rel, rerr)
		}
		sum := sha256.Sum256(data)
		lines = append(lines, rel+"\x00"+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return treeHashPrefix + hex.EncodeToString(sum[:]), nil
}

// PluginEntryHash pins an entry-only plugin — one with no cached plugin.json or
// component bodies — over its marketplace entry definition. The entry-prefix
// tag tells verify to skip recomputation (the entry isn't available there).
func PluginEntryHash(entry PluginEntry) (string, error) {
	b, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return entryHashPrefix + hex.EncodeToString(sum[:]), nil
}
