package semdiff

// ChangeKind classifies what happened to a symbol.
type ChangeKind string

const (
	Added             ChangeKind = "added"
	Removed           ChangeKind = "removed"
	Renamed           ChangeKind = "renamed"
	SignatureChanged  ChangeKind = "signature_changed"
	VisibilityChanged ChangeKind = "visibility_changed"
	BodyChanged       ChangeKind = "body_changed"
)

// FieldDelta describes a single property change on a symbol.
// Examples: {Field: "param_types", Old: "[string]", New: "[string, number]"}
//
//	{Field: "return_type", Old: "void", New: "Promise<Result>"}
//	{Field: "is_exported", Old: "true", New: "false"}
type FieldDelta struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// SymbolChange is the core output unit — one per changed symbol.
type SymbolChange struct {
	Name          string       `json:"name"`
	QualifiedName string       `json:"qualified_name"`
	Label         string       `json:"label"`                    // "Function", "Class", etc.
	FilePath      string       `json:"file"`
	Kind          ChangeKind   `json:"kind"`
	IsBreaking    bool         `json:"is_breaking"`
	OldName       string       `json:"old_name,omitempty"`  // only for renames
	Deltas        []FieldDelta `json:"deltas,omitempty"`    // what specifically changed
	Summary       string       `json:"summary"`             // human-readable one-liner
}

// FileChangeSummary groups symbol changes by file with file-level metadata.
type FileChangeSummary struct {
	Path    string         `json:"path"`
	Status  string         `json:"status"` // M, A, D, R
	OldPath string         `json:"old_path,omitempty"`
	Changes []SymbolChange `json:"changes"`
}

// SemanticDiffResult is the top-level output of the tool.
type SemanticDiffResult struct {
	Files           []FileChangeSummary `json:"files"`
	BreakingChanges []SymbolChange      `json:"breaking_changes,omitempty"`
	Impact          []ImpactEntry       `json:"impact,omitempty"`
	Summary         DiffSummary         `json:"summary"`
	Warnings        []string            `json:"warnings,omitempty"`
}

// ImpactEntry is a downstream symbol affected by a change (reuses detect_changes risk model).
type ImpactEntry struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	File      string `json:"file"`
	Risk      string `json:"risk"`       // CRITICAL, HIGH, MEDIUM, LOW
	Hop       int    `json:"hop"`
	ChangedBy string `json:"changed_by"` // which changed symbol caused this
}

// ---------------------------------------------------------------------------
// Commit planning types
// ---------------------------------------------------------------------------

// CommitGroup is a suggested commit boundary with draft message.
type CommitGroup struct {
	Files     []string       `json:"files"`
	Summaries []string       `json:"summaries"`             // compact one-liner per change
	Details   []ChangeDetail `json:"details,omitempty"`      // old→new for structural changes only
	Breaking  []string       `json:"breaking,omitempty"`     // names of breaking symbols
	TestCount int            `json:"test_count,omitempty"`   // collapsed test count
	Scope     string         `json:"scope"`                  // derived from common path prefix
	DraftMsg  string         `json:"draft_message"`          // suggested commit message
	Reason    string         `json:"reason,omitempty"`       // why these changes are grouped
}

// ChangeDetail provides old→new for structural changes that an LLM needs to
// see to write an accurate commit message. Body-only changes are omitted.
type ChangeDetail struct {
	Name   string       `json:"name"`
	Kind   ChangeKind   `json:"kind"`
	Deltas []FieldDelta `json:"deltas,omitempty"`
}

// CommitPlan is the top-level output of plan_commits.
type CommitPlan struct {
	Commits []CommitGroup `json:"commits"`
	Stats   PlanStats     `json:"stats"`
}

// PlanStats provides aggregate info about the plan.
type PlanStats struct {
	TotalChanges int `json:"total_changes"`
	TotalFiles   int `json:"total_files"`
	TotalCommits int `json:"total_commits"`
	Breaking     int `json:"breaking"`
}

// CouplingEdge represents a graph relationship between two changed symbols.
// Used to determine which changes belong in the same commit.
type CouplingEdge struct {
	FromQN string
	ToQN   string
	Type   string // "CALLS", "USAGE", "TESTS"
}

// DiffSummary provides aggregate counts.
type DiffSummary struct {
	FilesChanged    int  `json:"files_changed"`
	SymbolsAdded    int  `json:"symbols_added"`
	SymbolsRemoved  int  `json:"symbols_removed"`
	SymbolsModified int  `json:"symbols_modified"`
	BreakingCount   int  `json:"breaking_count"`
	ImpactedCount   int  `json:"impacted_count"`
	HasCrossService bool `json:"has_cross_service"`
}
