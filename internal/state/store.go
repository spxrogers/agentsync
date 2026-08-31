package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spxrogers/agentsync/internal/iox"
)

// rawTargets is the wire shape of targets.json with UNTYPED map keys. Load
// decodes into it first because a v1 document's keys ("claude:user::x") cannot
// unmarshal into map[Key]…; migrate converts them.
type rawTargets struct {
	SchemaVersion int                    `json:"schema_version"`
	Files         map[string]FileEntry   `json:"files,omitempty"`
	Keys          map[string]KeyEntry    `json:"keys,omitempty"`
	Marketplaces  map[string]Marketplace `json:"marketplaces,omitempty"`
	Plugins       map[string]PluginEntry `json:"plugins,omitempty"`
}

// Load reads targets.json from path. If the file is missing, returns a fresh
// state at the current SchemaVersion. Keys are upgraded to the current encoding
// on the way in (see migrate).
func Load(path string) (*Targets, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return New(), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw rawTargets
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// A file that is valid JSON but empty-shaped (`null`, `{}`, or any doc
	// with no schema_version AND no entries) is almost certainly corruption
	// (an interrupted external edit, a zeroed disk block, a truncate-clobber)
	// — agentsync itself always writes schema_version. Loading it as a
	// pristine empty state would make the next apply treat every managed
	// destination as unowned and back them all up as foreign collisions.
	// A legacy v0 file with real entries is still accepted (migrate handles it).
	if raw.SchemaVersion == 0 && len(raw.Files) == 0 && len(raw.Keys) == 0 &&
		len(raw.Marketplaces) == 0 && len(raw.Plugins) == 0 {
		return nil, fmt.Errorf("state file %s is empty or corrupt (no schema_version and no entries); "+
			"remove it to reinitialize (agentsync will re-adopt destinations on the next apply)", path)
	}
	t, err := migrate(&raw)
	if err != nil {
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	// Files and Keys are always allocated by migrate; Marketplaces and Plugins
	// are copied straight from the decoded document and are nil when the file
	// omits them, so only those two need a guard.
	if t.Marketplaces == nil {
		t.Marketplaces = map[string]Marketplace{}
	}
	if t.Plugins == nil {
		t.Plugins = map[string]PluginEntry{}
	}
	return t, nil
}

// ErrNonUTF8Key marks a state key that cannot be persisted because one of its
// fields is not valid UTF-8.
//
// The key ENCODING is byte-safe — ParseKey(k.String()) == k for any bytes at all
// — but the container is narrower: targets.json is JSON, and encoding/json
// rewrites every invalid byte in a string to U+FFFD. A key holding a raw 0xFF
// (Linux paths are byte strings, so $HOME, a project root or a filename may
// legally contain one) would be written with a length prefix that no longer
// matches its payload, and the next Load would refuse the WHOLE file — a state
// that does not converge, because the next apply rebuilds and re-writes the same
// mangled key. Save therefore fails closed at the moment the key is created,
// naming the destination path so the user can rename it.
//
// It fires from Save, which every caller reaches AFTER doing its work (apply has
// already written every destination), so the message also says so: the files
// landed, only the ownership record did not. Validating at plan time instead was
// considered and declined — it would reshape the apply pipeline for a failure
// mode that already self-heals on the next run.
var ErrNonUTF8Key = errors.New("state key field is not valid UTF-8")

// checkKeysEncodable rejects every key in t whose fields are not all valid
// UTF-8, BEFORE json.Marshal can silently rewrite them (see ErrNonUTF8Key).
// Offenders are reported sorted, each naming the destination path and the
// offending field, so the message is actionable rather than just diagnostic.
func checkKeysEncodable(t *Targets) error {
	var bad []string
	check := func(kind string, k Key) {
		for _, f := range [5]struct{ name, value string }{
			{"agent", k.Agent},
			{"scope", k.Scope},
			{"project", k.Project},
			{"path", k.Path},
			{"pointer", k.Pointer},
		} {
			if !utf8.ValidString(f.value) {
				bad = append(bad, fmt.Sprintf("%s entry for destination path %q: field %s = %q",
					kind, k.Path, f.name, f.value))
				return
			}
		}
	}
	for k := range t.Files {
		check("files", k)
	}
	for k := range t.Keys {
		check("keys", k)
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	// Say where in the run this lands. checkKeysEncodable fires from Save, which
	// every caller reaches AFTER doing its work, so a user hitting this has a
	// half-recorded machine and no way to tell from "rename it and re-run"
	// whether their destinations were written. They were.
	return fmt.Errorf("%w:\n  %s\nagentsync cannot record a destination whose path is not valid UTF-8; "+
		"rename it (or drop it from this agent's config) and re-run.\n"+
		"This check runs when state is SAVED, which is after the run has already done its work: "+
		"anything this run wrote is on disk and correct — only the ownership record was not updated. "+
		"Re-running after the rename converges (a destination agentsync does not own is backed up to "+
		".state/backups/ and re-adopted), but until then every attempt adds another backup",
		ErrNonUTF8Key, strings.Join(bad, "\n  "))
}

// Save serializes t to path atomically (iox.AtomicWrite).
func Save(path string, t *Targets) error {
	if t == nil {
		return fmt.Errorf("save nil targets")
	}
	// Fail closed BEFORE writing anything: json.Marshal would rewrite an invalid
	// UTF-8 byte in a key to U+FFFD and produce a file the next Load refuses.
	if err := checkKeysEncodable(t); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal targets: %w", err)
	}
	return iox.AtomicWrite(path, append(data, '\n'), 0o644)
}
