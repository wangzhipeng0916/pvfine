// Package extraction builds and queries the reference graph of a PVF archive.
//
// The archive is treated as a directed graph: script entries reference other
// entries either by a registry identifier (a row in a .lst list) or by a
// literal backticked path. Catalog declares which section positions carry
// references; ScanFile extracts them; Closure walks the result on demand.
package extraction

import "fmt"

// TargetKind classifies what a reference points at.
type TargetKind string

const (
	// TargetEntry is an archive entry addressed by a literal path.
	TargetEntry TargetKind = "entry"
	// TargetRegistry is an archive entry addressed through a .lst identifier.
	TargetRegistry TargetKind = "registry"
	// TargetAsset is a client image outside the archive. It is recorded but
	// never followed during closure.
	TargetAsset TargetKind = "asset"
	// TargetSectionIndex is a value that addresses a record inside another
	// entry's section — neither a path nor a .lst row. The knowledge base gives
	// a real case: a map's `[hellparty]` group_id closes inside
	// `etc/hellparty.etc [group index]`, whose group then lists members.
	TargetSectionIndex TargetKind = "section-index"
	// TargetSectionTable is a value that addresses a repeated block inside one
	// entry: `[equipment part set]` blocks in etc/equipmentpartset.etc each start
	// with the set number and carry that set's `.equ` path two tokens later. It
	// is neither a path nor a .lst row, and unlike a section index it has no
	// member kinds — just a key column and a value column.
	TargetSectionTable TargetKind = "section-table"
)

// RefState is the resolution outcome of one reference. Unmodeled exists so
// that an under-declared catalog is observable instead of silently lossy.
type RefState uint8

const (
	// StateResolved means the reference produced an archive entry.
	StateResolved RefState = iota
	// StateMissing means the reference is well formed but its target cannot
	// be located in this archive.
	StateMissing
	// StateUnknown marks a dynamic path (containing %d or *) that static
	// analysis cannot decide; it must not be downgraded to StateMissing.
	StateUnknown
	// StateUnmodeled marks a reference-shaped token whose section has no
	// declared edge.
	StateUnmodeled
)

func (s RefState) String() string {
	switch s {
	case StateResolved:
		return "resolved"
	case StateMissing:
		return "missing"
	case StateUnknown:
		return "unknown"
	case StateUnmodeled:
		return "unmodeled"
	}
	return fmt.Sprintf("refstate(%d)", uint8(s))
}

// Ref is one reference extracted from a script entry.
type Ref struct {
	// EdgeID is the catalog edge that produced this reference, or "" for an
	// unmodeled candidate.
	EdgeID string
	Kind   TargetKind
	// Value is the raw token text: an identifier or a path.
	Value string
	// Section is the innermost section name the token appeared in.
	Section string
	// Relation is the registry the value was resolved against. A payload or
	// nested-block reference can resolve through a different relation than its
	// parent edge, so the reference has to carry it — otherwise a fragment
	// would look the id up in the wrong table.
	Relation string
	// Line is the 0-based source line inside the section body, or -1 when the
	// catalog edge did not need line grouping.
	Line int
	// Target is the archive index, or -1 when unresolved.
	Target int32
	// TargetPath is the canonical archive path when resolved, otherwise the
	// location that was tried.
	TargetPath string
	State      RefState
	// Note carries diagnostics for non-resolved states.
	Note string
}

// Followable reports whether closure should enqueue this reference.
func (r Ref) Followable() bool {
	return r.State == StateResolved && r.Kind != TargetAsset && r.Target >= 0
}

// Unmodeled is a reference-shaped token that no catalog edge claims. It is the
// work queue that grows the catalog.
type Unmodeled struct {
	Extension string
	Section   string
	// Shape is "path" for backticked paths (high confidence) or "numeric" for
	// integer tokens (low confidence: coordinates look identical to ids).
	Shape     string
	TokenType int32
	Value     string
}

// ScanResult holds the references found in one archive entry.
type ScanResult struct {
	FileIndex int32
	Path      string
	Refs      []Ref
	Unmodeled []Unmodeled
	// Tokens counts value tokens visited in the entry.
	Tokens int
	// TextBytes is the decompiled size that was parsed.
	TextBytes int
	// Vocabulary counts text-bearing tokens (paths, tags, names) seen in this
	// entry; Consumed counts those a declared edge actually claimed. The pair
	// measures how much of the archive's own vocabulary the catalog explains,
	// which — unlike a ratio over the scanner's own output — cannot be improved
	// by changing the scanner's bookkeeping.
	Vocabulary int
	Consumed   int
}
