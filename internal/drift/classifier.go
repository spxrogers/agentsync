// Package drift classifies (source, applied, destination) hash triples per the
// 9-case table in the agentsync design spec. Pure function, no IO.
package drift

type Class int

const (
	Clean Class = iota
	Pending
	Drift
	Converged
	Conflict
	New
	ForeignCollision
	Orphan
	OrphanDrifted
)

func (c Class) String() string {
	switch c {
	case Clean:
		return "clean"
	case Pending:
		return "pending"
	case Drift:
		return "drift"
	case Converged:
		return "converged"
	case Conflict:
		return "conflict"
	case New:
		return "new"
	case ForeignCollision:
		return "foreign-collision"
	case Orphan:
		return "orphan"
	case OrphanDrifted:
		return "orphan-drifted"
	}
	return "unknown"
}

// Classify returns the case for one tracked item. Empty string means "absent."
// hsrc=src hash now; happlied=last-applied hash; hdest=on-disk hash now.
func Classify(hsrc, happlied, hdest string) Class {
	switch {
	case happlied == "" && hdest == "" && hsrc != "":
		return New
	case happlied == "" && hdest != "" && hsrc != "":
		return ForeignCollision
	case happlied != "" && hsrc == "":
		if hdest == happlied {
			return Orphan
		}
		return OrphanDrifted
	case hsrc == happlied && hdest == happlied:
		return Clean
	case hsrc != happlied && hdest == happlied:
		return Pending
	case hsrc == happlied && hdest != happlied:
		return Drift
	case hsrc != happlied && hdest != happlied && hsrc == hdest:
		return Converged
	default:
		return Conflict
	}
}

// SafeForAutoApply returns true for cases apply can resolve without prompting.
func SafeForAutoApply(c Class) bool {
	return c == Clean || c == Pending || c == New || c == Converged
}
