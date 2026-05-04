package drift_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/drift"
)

func TestClassify(t *testing.T) {
	type tc struct {
		name                  string
		hsrc, happlied, hdest string // "" = nil
		want                  drift.Class
	}
	cases := []tc{
		{name: "clean", hsrc: "a", happlied: "a", hdest: "a", want: drift.Clean},
		{name: "pending", hsrc: "b", happlied: "a", hdest: "a", want: drift.Pending},
		{name: "drift", hsrc: "a", happlied: "a", hdest: "b", want: drift.Drift},
		{name: "converged", hsrc: "b", happlied: "a", hdest: "b", want: drift.Converged},
		{name: "conflict", hsrc: "b", happlied: "a", hdest: "c", want: drift.Conflict},
		{name: "new", hsrc: "a", happlied: "", hdest: "", want: drift.New},
		{name: "foreign-collision", hsrc: "a", happlied: "", hdest: "x", want: drift.ForeignCollision},
		{name: "orphan", hsrc: "", happlied: "a", hdest: "a", want: drift.Orphan},
		{name: "orphan-drifted", hsrc: "", happlied: "a", hdest: "b", want: drift.OrphanDrifted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := drift.Classify(c.hsrc, c.happlied, c.hdest)
			if got != c.want {
				t.Fatalf("Classify(%q,%q,%q) = %v, want %v", c.hsrc, c.happlied, c.hdest, got, c.want)
			}
		})
	}
}

func TestSafeForAutoApply(t *testing.T) {
	safe := []drift.Class{drift.Clean, drift.Pending, drift.New, drift.Converged}
	unsafe := []drift.Class{drift.Drift, drift.Conflict, drift.ForeignCollision, drift.Orphan, drift.OrphanDrifted}

	for _, c := range safe {
		if !drift.SafeForAutoApply(c) {
			t.Errorf("expected SafeForAutoApply(%v) = true", c)
		}
	}
	for _, c := range unsafe {
		if drift.SafeForAutoApply(c) {
			t.Errorf("expected SafeForAutoApply(%v) = false", c)
		}
	}
}
