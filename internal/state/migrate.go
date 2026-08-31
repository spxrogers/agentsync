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
//     encodings; each is parsed AND role-checked (parseCurrentKey), so a
//     hand-edited or corrupted file fails loudly instead of silently disowning a
//     destination.
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
// A key that reached maxLegacyReadings is refused too, but it gets a DIFFERENT
// paragraph. The cap fires mid-enumeration, so agentsync never learns how many
// readings that key had — calling it ambiguous would tell the user agentsync
// declined to choose between readings when in fact it declined to finish
// counting them, and the key may well have had exactly one (or none). See
// errLegacyEnumerationCapped.
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
		parseFile = func(s string) (Key, error) { return parseCurrentKey(s, false) }
		parsePointer = func(s string) (Key, error) { return parseCurrentKey(s, true) }
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
	ambiguous, capped := false, false
	// The line is the error alone: EVERY error parseFile/parsePointer can return
	// already names the offending key, quoted with %q (legacy.go, key.go,
	// parseCurrentKey) — so prefixing the key again here printed it twice per
	// line, three times when the message also quotes the field that failed.
	// That %q is a CONTRACT, not a coincidence, and a new error constructor in
	// those files must keep it. Map keys come VERBATIM from targets.json, which
	// is a hand-editable file: an unsanitized key carrying ESC or CR would reach
	// the terminal through whichever sink prints this error (ui.WarnWriter
	// passes lines 2..n of a multi-line message through untouched, and this
	// refusal is ALWAYS multi-line), and a key carrying a newline could forge
	// whole extra output lines even through a SANITIZING sink, since
	// sanitization preserves newlines. %q escapes all three and needs no import.
	note := func(err error) {
		// Two sentinels, two paragraphs — never folded into one. errors.Is on
		// each rather than a switch on the first match, so a file holding both
		// kinds of bad key gets both explanations.
		if errors.Is(err, errAmbiguousLegacyKey) {
			ambiguous = true
		}
		if errors.Is(err, errLegacyEnumerationCapped) {
			capped = true
		}
		bad = append(bad, err.Error())
	}
	for s, entry := range raw.Files {
		k, err := parseFile(s)
		if err != nil {
			note(err)
			continue
		}
		out.Files[k] = entry
	}
	for s, entry := range raw.Keys {
		k, err := parsePointer(s)
		if err != nil {
			note(err)
			continue
		}
		out.Keys[k] = entry
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		remedy := "each key above is shown Go-quoted, so a backslash appears doubled and a control byte " +
			"as an escape — unquote it before searching targets.json.\n" +
			"targets.json is bookkeeping only; no configuration lives here. Delete those entries " +
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
		if capped {
			// Deliberately NOT the ambiguity paragraph. An entry refused by the
			// cap was never read to the end, so "agentsync refuses to guess
			// between the readings" would be a claim about a reading set that
			// was never built — and for a key with one reading (or none) it is
			// simply false. Say what actually happened instead.
			remedy += ".\nAn entry reported as having too many candidates is one whose v1 encoding packs " +
				"enough ':' bytes into a single key that listing its possible readings is superlinear " +
				"work; unbounded, a hand-edited targets.json of a few KB could exhaust memory in every " +
				"command that loads state. agentsync stops counting at the cap named on that line, so it " +
				"does not know whether the key had one reading, several or none — it is refused unread, " +
				"and deleting it costs only the re-adopt above"
		}
		return nil, fmt.Errorf("%d state key(s) could not be read at schema_version=%d:\n  %s\n%s",
			len(bad), raw.SchemaVersion, strings.Join(bad, "\n  "), remedy)
	}
	return out, nil
}

// parseCurrentKey decodes a current-encoding key and enforces its ROLE: a
// Targets.Files key names a whole file and must carry NO pointer; a Targets.Keys
// key names one JSON-pointer-addressable key inside a shared file and must carry
// one.
//
// The v1 parser enforces that structurally — parseLegacyPointerKey only produces
// readings that contain a ":/" boundary, and parseLegacyFileKey never splits one
// off — so without this check the CURRENT schema would be LAXER than the one it
// replaced: a hand-edited v2 file could file a pointered key under "files" (its
// whole-file entry would then own a path it does not write) or an empty-Pointer
// key under "keys" (which reaches jsonkeys.MergeKeys as the pointer "", where
// pointerExists is vacuously true and the entry is never pruned). Both load
// silently today. Refusing costs the user only the cheap re-adopt every other
// refusal in migrate offers.
func parseCurrentKey(s string, pointered bool) (Key, error) {
	k, err := ParseKey(s)
	if err != nil {
		return Key{}, err
	}
	if pointered && k.Pointer == "" {
		return Key{}, fmt.Errorf("state key %q: a keys entry must name a JSON pointer, but its pointer field is empty", s)
	}
	if !pointered && k.Pointer != "" {
		return Key{}, fmt.Errorf("state key %q: a files entry names a whole file and must not carry a JSON pointer (has %q)", s, k.Pointer)
	}
	return k, nil
}
