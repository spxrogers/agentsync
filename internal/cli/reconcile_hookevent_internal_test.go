package cli

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestCanonicalHookEvent pins the inversion contract at the moment it stopped
// building its own registry (#232). Passing the caller's registry cannot change
// the answer — it is the same registryFactory() product either way — but the
// old spelling made two facts implicit that the table below makes explicit:
//
//   - a NON-renaming adapter (claude, codex: no HookEventNamer) passes the
//     native segment straight through, because the comma-ok assertion fails; and
//   - an UNREGISTERED agent takes exactly the same branch, because
//     Registry.Lookup returns a nil adapter.Adapter and a comma-ok assertion on
//     a nil interface yields (nil, false) rather than panicking. That was
//     nil-safe only by Go's assertion semantics, never by an explicit guard, and
//     nothing asserted it.
//   - a NIL registry is guarded explicitly (Registry.Lookup dereferences its
//     receiver) and takes that same passthrough; the precondition is a non-nil
//     registry, and the guard is what a caller that forgot to set one gets.
//
// The renaming rows are the ones that must keep working: a gemini
// `/hooks/BeforeTool` pointer has to resolve to hooks/PreToolUse.toml, or
// reconcile's write-back and `explain <path>#<pointer>` name a file that does
// not exist.
func TestCanonicalHookEvent(t *testing.T) {
	reg := registryFactory()
	events := []string{"PreToolUse", "PostToolUse"}

	tests := []struct {
		name   string
		nilReg bool // pass a nil *Registry instead of reg
		agent  string
		native string
		want   string
		wantOK bool
	}{
		{name: "claude does not rename", agent: "claude", native: "PreToolUse", want: "PreToolUse", wantOK: true},
		{name: "codex does not rename", agent: "codex", native: "PostToolUse", want: "PostToolUse", wantOK: true},
		{name: "gemini renames", agent: "gemini", native: "BeforeTool", want: "PreToolUse", wantOK: true},
		{name: "cursor renames", agent: "cursor", native: "preToolUse", want: "PreToolUse", wantOK: true},
		{name: "renaming agent, unknown native spelling", agent: "gemini", native: "NoSuchEvent"},
		{name: "unregistered agent passes through", agent: "no-such-agent", native: "PreToolUse", want: "PreToolUse", wantOK: true},
		{name: "empty agent passes through", agent: "", native: "PreToolUse", want: "PreToolUse", wantOK: true},
		{name: "nil registry passes through", nilReg: true, agent: "gemini", native: "BeforeTool", want: "BeforeTool", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := reg
			if tc.nilReg {
				r = nil
			}
			got, ok := canonicalHookEvent(r, tc.agent, tc.native, events)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("canonicalHookEvent(%q, %q) = (%q, %v), want (%q, %v)",
					tc.agent, tc.native, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestCanonicalHookEvent_UnregisteredLookupIsNil is the half of the above the
// table cannot show: WHY an unregistered agent is safe. If Lookup ever returned
// a non-nil zero adapter, the comma-ok assertion could start succeeding and the
// passthrough row above would silently change meaning.
func TestCanonicalHookEvent_UnregisteredLookupIsNil(t *testing.T) {
	a := registryFactory().Lookup("no-such-agent")
	if a != nil {
		t.Fatalf("Registry.Lookup of an unregistered name = %T, want a nil adapter.Adapter: "+
			"canonicalHookEvent's passthrough depends on the comma-ok assertion failing", a)
	}
	if _, ok := a.(adapter.HookEventNamer); ok {
		t.Fatal("the comma-ok assertion on Lookup's nil result reported ok=true")
	}
}
