package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// symlinkRefusedSentinel is hashFile's answer for a symlink this configuration
// does not read through (destReadPath). Opaque: it exists only to never equal
// a content hash, and diff keys its symlink hunk on the same value
// (planItem.destSymlinkRefused), so the two sites must agree on it.
const symlinkRefusedSentinel = "symlink-not-regular-file"

// shapeSentinel is hashFile's answer for a destination readDestBytes refuses:
// present and not a regular file (FIFO, device, socket, directory), or
// unstattable. diff keys its shape hunk on it.
const shapeSentinel = "not-a-regular-file"

// symlinkUnresolvableSentinel is hashFile's answer for a symlink the user opted
// into reading through that does not resolve (dangling, loop). Equally opaque;
// a different token only so diff and reconcile can give the right advice.
const symlinkUnresolvableSentinel = "symlink-target-unresolvable"

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFile returns the SHA-256 hex digest of the file at path. Returns
// the empty string when the destination cannot be read as content at all —
// absent, or present-and-unreadable — which `drift.Classify` reads as "absent",
// the expected signal for Orphan / OrphanDrifted. A destination whose SHAPE is
// wrong, or which cannot be stat'd, answers the opaque marker below instead.
//
// A SYMLINK at the path answers symlinkRefusedSentinel unless
// AGENTSYNC_ALLOW_SYMLINK_DEST=1 (destReadPath — the gate apply writes under).
// The sentinel is a whole-file-only policy signal: a managed regular file
// became a link you have not opted into. Reading through such a link and
// comparing hashes, as this once did, made the swap invisible to status. With
// the env set the link is resolved and its TARGET hashed — the file apply
// converges — so a chezmoi setup reports clean after a successful apply
// instead of a drift no apply can clear. Opting in never lets a non-regular
// target through: a link to one (`ln -s /dev/null`) resolves and then answers
// the SHAPE sentinel below, with the switch set or unset — the switch cannot
// help there; a dangling or looping link answers symlinkUnresolvableSentinel
// once opted in (unset, it is refused like any other link), mirroring apply's
// "resolve symlink" failure.
func hashFile(path string) string {
	p, why := destReadPath(path)
	switch why {
	case symlinkRefusedByEnv:
		// The link target is deliberately NOT part of either sentinel: it is
		// attacker-choosable, and a sentinel must stay a stable opaque token
		// that never equals a content hash.
		return symlinkRefusedSentinel
	case symlinkUnresolvable:
		return symlinkUnresolvableSentinel
	}
	path = p
	// The shape rule itself lives in readDestBytes, the one gate every
	// destination read in this package passes through; this function maps its
	// refusal onto a sentinel — distinct from the symlink ones, so a diagnostic
	// never calls a FIFO a symlink — rather than re-deciding it.
	data, err := readDestBytes(path)
	if err != nil {
		// Both refusals map to the SAME opaque token, deliberately. These
		// sentinels exist to never equal a content hash; diff's shape hunk,
		// which keys prose on this one, words it for both facts (reconcile's
		// prompt shows the token itself). Before this gate existed the
		// predicate answered false for an unstattable destination too, so
		// splitting them here would move a parent-ENOTDIR dest from
		// ForeignCollision to New — and New is SafeForAutoApply. A plain read
		// failure still answers "", as it did.
		if errors.Is(err, errDestNotRegular) || errors.Is(err, errDestUnstattable) {
			return shapeSentinel
		}
		return ""
	}
	return hashContent(data)
}

func hashAnyValue(v any) string {
	if v == nil {
		return ""
	}
	data, _ := json.Marshal(v)
	return hashContent(data)
}
