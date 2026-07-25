package jsonkeys_test

import (
	"encoding/json"
	"testing"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// TestDecodeObject_LaxEdges pins DecodeObject's documented edge semantics
// (round-1 review: two lenses flagged the laxity vs json.Unmarshal as
// undocumented). These are load-bearing for every JSON config read — adapter
// ingests and the key-merge machinery share this decoder — so a change in
// either direction must be a deliberate one that shows up here.
func TestDecodeObject_LaxEdges(t *testing.T) {
	// Empty input is an empty config, not an error.
	m, err := jsonkeys.DecodeObject(nil)
	if err != nil || len(m) != 0 {
		t.Fatalf("nil input: want empty map, got %v / err %v", m, err)
	}

	// Trailing content after the first top-level value is ignored
	// (json.Decoder semantics): the first object decodes, the rest is unread.
	m, err = jsonkeys.DecodeObject([]byte(`{"a": 1}{"b": 2}`))
	if err != nil {
		t.Fatalf("trailing content must be tolerated, got err %v", err)
	}
	if m["a"] != json.Number("1") || len(m) != 1 {
		t.Fatalf("first object should decode alone, got %v", m)
	}

	// A corrupt single value still errors — laxity is about framing, not
	// about accepting broken JSON.
	if _, err := jsonkeys.DecodeObject([]byte(`{"a":`)); err == nil {
		t.Fatal("truncated object must error")
	}
	// A non-object top level errors: this decoder reads config OBJECTS.
	if _, err := jsonkeys.DecodeObject([]byte(`[1]`)); err == nil {
		t.Fatal("non-object top level must error")
	}
}
