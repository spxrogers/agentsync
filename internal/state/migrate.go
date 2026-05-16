package state

import "fmt"

// migrate brings a loaded Targets up to SchemaVersion. Today the only
// recognized states are:
//   - schema_version == 0 (legacy implicit-v1 file, written before the
//     field was stamped): upgrade in place to current.
//   - schema_version == SchemaVersion: nothing to do.
//
// A higher schema_version means a newer agentsync binary wrote this
// file and the current binary cannot safely interpret it. We refuse
// rather than silently treat unknown future fields as missing.
//
// When v2 ships, add a case here that walks v1 fields into the v2
// shape. The Save() side stamps SchemaVersion = current, so post-
// migration writes commit the upgrade.
func migrate(t *Targets) error {
	switch t.SchemaVersion {
	case 0:
		t.SchemaVersion = SchemaVersion
	case SchemaVersion:
		// no-op
	default:
		if t.SchemaVersion > SchemaVersion {
			return fmt.Errorf("state schema_version=%d is newer than this binary supports (%d); "+
				"upgrade agentsync or remove ~/.agentsync/.state/targets.json to start fresh",
				t.SchemaVersion, SchemaVersion)
		}
		// Older but non-zero — no migrator registered yet. Bail loud so
		// the user can decide rather than silently treating it as
		// current.
		return fmt.Errorf("no migrator registered for state schema_version=%d (current=%d)",
			t.SchemaVersion, SchemaVersion)
	}
	return nil
}
