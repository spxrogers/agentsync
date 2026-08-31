package state

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// migrate converts a decoded targets.json (rawTargets, string-keyed) into the
// current typed Targets, upgrading the key encoding on the way:
//
//   - schema_version 0 (legacy implicit v1, written before the field was
//     stamped) or 1: every files/keys key is read with the v1 parser
//     (legacy.go) and re-keyed as a state.Key. The version is stamped to
//     SchemaVersion; the next Save commits the upgrade.
//   - schema_version == SchemaVersion: keys must already be state.Key
//     encodings; each is parsed, so a hand-edited or corrupted file fails loudly
//     instead of silently disowning a destination.
//   - A higher schema_version means a newer agentsync binary wrote this file and
//     the current binary cannot safely interpret it. We refuse rather than
//     silently treat unknown future fields as missing.
//
// A v1 key with more than one possible reading is REFUSED, never guessed: a
// wrong project/path split records the entry under the wrong project, which is
// exactly the over-disown bug the typed key exists to remove. targets.json holds
// no configuration, so the remedy is cheap and the message says so — this is the
// user-facing remedy errAmbiguousLegacyKey's doc comment points at, and an
// ambiguous reading gets its own paragraph explaining why nothing is guessed.
//
// Marketplaces and Plugins keys are marketplace names and "owner/plugin" ids —
// a different namespace — and are carried across verbatim.
//
// When v3 ships, add a case here.
func migrate(raw *rawTargets) (*Targets, error) {
	out := &Targets{
		SchemaVersion: SchemaVersion,
		Files:         make(map[Key]FileEntry, len(raw.Files)),
		Keys:          make(map[Key]KeyEntry, len(raw.Keys)),
		Marketplaces:  raw.Marketplaces,
		Plugins:       raw.Plugins,
	}

	var parseFile, parsePointer func(string) (Key, error)
	switch raw.SchemaVersion {
	case 0, 1:
		parseFile, parsePointer = parseLegacyFileKey, parseLegacyPointerKey
	case SchemaVersion:
		parseFile, parsePointer = ParseKey, ParseKey
	default:
		if raw.SchemaVersion > SchemaVersion {
			return nil, fmt.Errorf("state schema_version=%d is newer than this binary supports (%d); "+
				"upgrade agentsync or remove ~/.agentsync/.state/targets.json to start fresh",
				raw.SchemaVersion, SchemaVersion)
		}
		return nil, fmt.Errorf("no migrator registered for state schema_version=%d (current=%d)",
			raw.SchemaVersion, SchemaVersion)
	}

	var bad []string
	ambiguous := false
	note := func(s string, err error) {
		if errors.Is(err, errAmbiguousLegacyKey) {
			ambiguous = true
		}
		bad = append(bad, fmt.Sprintf("%s — %v", s, err))
	}
	for s, entry := range raw.Files {
		k, err := parseFile(s)
		if err != nil {
			note(s, err)
			continue
		}
		out.Files[k] = entry
	}
	for s, entry := range raw.Keys {
		k, err := parsePointer(s)
		if err != nil {
			note(s, err)
			continue
		}
		out.Keys[k] = entry
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		remedy := "targets.json is bookkeeping only; no configuration lives here. Delete those entries " +
			"(or the whole file) and re-run — the next `agentsync apply` re-adopts each destination, " +
			"backing up any pre-existing content to .state/backups/ first, so nothing is lost"
		if ambiguous {
			// Only the ambiguous case has a second thing to say: the entry is
			// readable, just not UNIQUELY, and agentsync will not pick one.
			remedy += ".\nAn entry reported as having several readings is one the v1 ':'-joined encoding " +
				"left genuinely ambiguous — only possible when a project root or a destination path " +
				"contains ':'. agentsync refuses to guess the project/path split because recording the " +
				"entry under the wrong project is exactly the over-disown bug this format change removes; " +
				"deleting it costs only the re-adopt above"
		}
		return nil, fmt.Errorf("%d state key(s) could not be read at schema_version=%d:\n  %s\n%s",
			len(bad), raw.SchemaVersion, strings.Join(bad, "\n  "), remedy)
	}
	return out, nil
}
