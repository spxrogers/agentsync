package state

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spxrogers/agentsync/internal/paths"
)

// keyFormatMarker prefixes every encoded state key. It versions the KEY
// ENCODING and is deliberately independent of SchemaVersion: a future
// state-schema bump for an unrelated reason must not be forced to change the key
// format, and a key-format change must not masquerade as a schema change. It
// also gives a legacy (v1) or hand-edited key a loud, immediate parse failure
// instead of a silent misreading.
const keyFormatMarker = "as1"

// Key identifies one managed destination in targets.json: a whole file
// (Pointer == "", stored in Targets.Files) or one JSON-pointer-addressable key
// inside a shared file (Pointer != "", stored in Targets.Keys).
//
// Project and Path are ${HOME}-relative (paths.HomeRelative) so targets.json is
// portable across machines whose $HOME differs. Scope is a plain string —
// adapter.Scope.String(), i.e. "user" or "project" — because internal/state sits
// below internal/adapter in the package layering and must not import it.
//
// Key is the ONLY way to name a state entry: Targets.Files and Targets.Keys are
// keyed by it, so building a key with fmt.Sprintf is a compile error rather than
// a convention someone has to remember. See String for why the encoding is what
// it is.
type Key struct {
	Agent   string
	Scope   string
	Project string
	Path    string
	Pointer string
}

// NewFileKey builds the whole-file key for a destination. project and dest are
// ABSOLUTE paths; both are portabilized against userHome — the user's $HOME
// (paths.HomeDir), NOT the agentsync home, because destination files live under
// $HOME. An empty project (user scope) portabilizes to "".
func NewFileKey(userHome, agent, scope, project, dest string) Key {
	return Key{
		Agent:   agent,
		Scope:   scope,
		Project: paths.HomeRelative(userHome, project),
		Path:    paths.HomeRelative(userHome, dest),
	}
}

// NewPointerKey builds the key for one JSON pointer inside a shared destination
// file. Arguments match NewFileKey; pointer is a JSON pointer
// ("/mcpServers/github") and is stored verbatim.
func NewPointerKey(userHome, agent, scope, project, dest, pointer string) Key {
	k := NewFileKey(userHome, agent, scope, project, dest)
	k.Pointer = pointer
	return k
}

// String encodes k as the targets.json map key.
//
// Fields are LENGTH-PREFIXED rather than joined by a separator — the same
// technique, for the same reason, as the hook-handler join key in internal/cli.
// The v1 format "agent:scope:project:path[:pointer]" assumed ':' could not occur
// inside a field, but ':' is a legal byte in a POSIX path: a project root
// containing one produced keys that string-prefixed a SIBLING project's keys, and
// a prefix-scoped prune then deleted the sibling's ownership (issue #227).
//
// Length prefixes make the encoding injective for ANY byte content: decoding
// reads a decimal length, a ':', then exactly that many bytes, never
// interpreting the bytes themselves. ParseKey is a total left inverse of String,
// which is what makes String injective.
func (k Key) String() string {
	var b strings.Builder
	b.WriteString(keyFormatMarker)
	for _, f := range [5]string{k.Agent, k.Scope, k.Project, k.Path, k.Pointer} {
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
	}
	return b.String()
}

// ParseKey decodes a key produced by String. It is strict: a legacy (v1) key, a
// non-canonical length ("06"), a truncated payload, or trailing bytes are all
// errors, never a best-effort reading. Load relies on that — a key it cannot
// decode exactly means the state file was hand-edited or corrupted, and
// agentsync refuses rather than silently disowning a destination.
func ParseKey(s string) (Key, error) {
	rest, ok := strings.CutPrefix(s, keyFormatMarker)
	if !ok {
		return Key{}, fmt.Errorf("state key %q: not an agentsync state key (missing %q marker)", s, keyFormatMarker)
	}
	var fields [5]string
	for i := range fields {
		body, ok := strings.CutPrefix(rest, "|")
		if !ok {
			return Key{}, fmt.Errorf("state key %q: field %d: missing '|' separator", s, i)
		}
		colon := strings.IndexByte(body, ':')
		if colon < 0 {
			return Key{}, fmt.Errorf("state key %q: field %d: missing length prefix", s, i)
		}
		digits := body[:colon]
		n, err := strconv.Atoi(digits)
		if err != nil || n < 0 || digits != strconv.Itoa(n) {
			return Key{}, fmt.Errorf("state key %q: field %d: bad length prefix %q", s, i, digits)
		}
		body = body[colon+1:]
		if len(body) < n {
			return Key{}, fmt.Errorf("state key %q: field %d: truncated (want %d bytes, have %d)", s, i, n, len(body))
		}
		fields[i] = body[:n]
		rest = body[n:]
	}
	if rest != "" {
		return Key{}, fmt.Errorf("state key %q: %d trailing byte(s) after the last field", s, len(rest))
	}
	return Key{
		Agent:   fields[0],
		Scope:   fields[1],
		Project: fields[2],
		Path:    fields[3],
		Pointer: fields[4],
	}, nil
}

// MarshalText lets Key be a JSON object key: encoding/json renders a map[Key]V
// as an ordinary string-keyed object, sorted by the marshalled text, so
// targets.json keeps the shape it has always had and stays byte-deterministic.
//
// The ENCODING is byte-safe, but the JSON container is narrower than the
// encoding: encoding/json rewrites an invalid UTF-8 byte in a string to U+FFFD,
// which would break the length prefix. Save refuses such a key before it ever
// reaches this boundary — see ErrNonUTF8Key.
func (k Key) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// UnmarshalText is the inverse of MarshalText, so a map[Key]V round-trips
// through encoding/json (TestKey_IsAJSONObjectKey); a key that does not decode
// fails the whole unmarshal.
//
// It exists to complete the encoding.TextMarshaler/TextUnmarshaler pair, not to
// guard Load: nothing in production unmarshals into a map[Key]V. Load decodes
// targets.json into rawTargets — string-keyed, because a v1 document's keys
// could not unmarshal into map[Key]V at all — and migrate re-keys it through
// ParseKey/parseCurrentKey, which is where the fail-closed role check lives.
func (k *Key) UnmarshalText(b []byte) error {
	parsed, err := ParseKey(string(b))
	if err != nil {
		return err
	}
	*k = parsed
	return nil
}

// AbsPath expands k.Path back to an absolute filesystem path. Callers that turn
// a state entry into a real file operation MUST route through this so they never
// operate on the literal "${HOME}/..." string.
func (k Key) AbsPath(userHome string) string { return paths.FromHomeRelative(userHome, k.Path) }

// InTree reports whether k was recorded by agent at scope for the given project
// tree. project is the PORTABLE (${HOME}-relative) root — "" at user scope —
// matching what NewFileKey stores, so callers hoist paths.HomeRelative out of
// their loop. This replaces the v1 "does the key string start with this prefix"
// test, which could not tell a sibling project apart when a root contained ':'.
func (k Key) InTree(agent, scope, project string) bool {
	return k.Agent == agent && k.Scope == scope && k.Project == project
}
