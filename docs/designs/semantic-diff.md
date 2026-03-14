# Semantic Diff — Design

## Overview

A new `semantic_diff` MCP tool that compares old and new versions of changed files at the AST level, producing structured, human-readable descriptions of *what* changed about each symbol (e.g., "parameter added", "return type changed", "export removed"). Builds on the existing `detect_changes` infrastructure (git diff parsing, line-to-symbol mapping, impact tracing) but adds dual-version extraction and symbol-level comparison.

## Summary for Review

- **Interpretation**: The user wants a tool that goes beyond "these lines changed" to "function X had parameter `priority: number` added and return type changed from `void` to `Promise<Result>`". It should detect added/removed/renamed symbols, signature changes, body-only changes, and flag breaking changes for exported symbols. Impact tracing (who calls the changed symbol) is reused from `detect_changes`.
- **Key decisions**:
  - Dual-version extraction: retrieve old file content via `git show`, parse both versions with the existing `cbm.ExtractFile`, then diff the two `Definition` slices.
  - Symbol matching by qualified name first, then fuzzy matching (same label + high body similarity) for rename detection.
  - New package `internal/semdiff` owns the comparison logic — keeps it testable and decoupled from MCP tool wiring.
  - Reuses `pipeline.ParseGitDiffFiles`, `pipeline.ParseGitDiffHunks`, `pipeline.runGit`, and `detect_changes`'s impact tracing wholesale.
  - Breaking change = exported symbol with signature/param/return type change, or exported symbol removed.
- **Assumptions**:
  - Cross-file moves are out of scope (80% case). A symbol removed from file A and added to file B is reported as "removed" + "added", not "moved".
  - Rename detection uses a simple heuristic (same label, similar line range or body hash) — not full GumTree-style tree matching.
  - The tool works on any language already supported by `cbm.ExtractFile` (64 languages), not just TypeScript.
- **Scope**: Symbol-level diff + classification + breaking change detection + impact tracing. Deferred: cross-file move detection, data-flow analysis, test-impact mapping, visual/HTML output.

## Conventions

- Go-idiomatic error handling: `if err != nil { return ..., fmt.Errorf("context: %w", err) }`
- Tool registration follows `registerDetectChanges()` pattern: one file, register function + handler
- Return results via `s.result(data)` for format-aware output (JSON/YAML)
- Use `slog.Debug`/`slog.Warn` for diagnostics, never `log.Printf`
- Tests colocated in `*_test.go`; integration tests use temporary git repos
- No panics; graceful degradation on parse failures (skip unparseable files, log warning)

## Architecture

### Subsystems

| # | Subsystem | Responsibility | Depends On | Files |
|---|-----------|---------------|------------|-------|
| 1 | Old Source Retrieval | Fetch old file contents via `git show` for a given diff scope | — | `internal/pipeline/gitshow.go` |
| 2 | Symbol Differ | Compare two `[]cbm.Definition` slices, produce `[]SymbolChange` | — | `internal/semdiff/differ.go` |
| 3 | Breaking Change Classifier | Classify which `SymbolChange`s are breaking based on export status + change kind | 2 | `internal/semdiff/breaking.go` |
| 4 | MCP Tool Handler | Wire everything together: git diff → old source → extract → diff → impact → format | 1, 2, 3 | `internal/tools/semantic_diff.go` |

## Shared Contracts

```go
// --- internal/semdiff/types.go ---

package semdiff

import "github.com/DeusData/codebase-memory-mcp/internal/cbm"

// ChangeKind classifies what happened to a symbol.
type ChangeKind string

const (
    Added            ChangeKind = "added"
    Removed          ChangeKind = "removed"
    Renamed          ChangeKind = "renamed"
    SignatureChanged ChangeKind = "signature_changed"
    BodyChanged      ChangeKind = "body_changed"
)

// FieldDelta describes a single property change on a symbol.
// Examples: {Field: "param_types", Old: "[string]", New: "[string, number]"}
//           {Field: "return_type", Old: "void", New: "Promise<Result>"}
//           {Field: "is_exported", Old: "true", New: "false"}
type FieldDelta struct {
    Field string `json:"field"`
    Old   string `json:"old"`
    New   string `json:"new"`
}

// SymbolChange is the core output unit — one per changed symbol.
type SymbolChange struct {
    Name          string       `json:"name"`
    QualifiedName string       `json:"qualified_name"`
    Label         string       `json:"label"`           // "Function", "Class", etc.
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

// --- internal/pipeline/gitshow.go (exported function) ---

// OldFileContents retrieves file contents from the base version for a given diff scope.
// Returns a map of relative path -> file content bytes.
// For scope "all": reads from HEAD.
// For scope "branch": reads from the base branch.
// For scope "staged"/"unstaged": reads from HEAD (the pre-change version).
// Files that don't exist in the old version (added files) are omitted from the map.
// Renamed files are keyed by their old path.
func OldFileContents(repoPath string, scope DiffScope, baseBranch string, files []ChangedFile) (map[string][]byte, error)
```

## Subsystem Details

### 1. Old Source Retrieval
**Files**: `internal/pipeline/gitshow.go`, `internal/pipeline/gitshow_test.go`

**Key decisions**:
- Use `git show <ref>:<path>` per file rather than checking out the old tree. This avoids touching the working tree and works with dirty state.
- The git ref is derived from scope: `HEAD` for staged/unstaged/all, `<baseBranch>` for branch scope. For renamed files, use `OldPath`.
- Added files (`Status == "A"`) produce no old content (skip). Deleted files (`Status == "D"`) produce old content but no new content.
- Parallelise `git show` calls with `errgroup` (bounded to `runtime.NumCPU()`). Each call has a 10-second timeout.
- On `git show` failure for a single file (e.g., binary, submodule), log warning and skip — don't abort the entire diff.

### 2. Symbol Differ
**Files**: `internal/semdiff/differ.go`, `internal/semdiff/differ_test.go`, `internal/semdiff/types.go`

**Key decisions**:
- **Matching strategy** (two passes):
  1. **Exact match**: Match old→new definitions by `QualifiedName` within the same file. This handles the common case (symbol exists in both versions).
  2. **Fuzzy match** (rename detection): Unmatched old definitions are compared against unmatched new definitions *of the same Label* in the same file. A rename is detected when exactly one candidate has high similarity (>70% of fields match: same param count, same return type, similar line count). This is deliberately conservative — false negatives are preferable to false positives.
- **Delta computation**: For matched pairs, compare field-by-field: `Signature`, `ParamTypes` (as joined strings), `ReturnType`, `IsExported`, `IsAbstract`, `Complexity`, `Lines`. Only changed fields produce a `FieldDelta`.
- **Kind classification**:
  - No deltas at all → `BodyChanged` (lines changed but no structural diff — e.g., implementation tweak)
  - Any delta in `Signature`, `ParamTypes`, or `ReturnType` → `SignatureChanged`
  - Fuzzy-matched with different name → `Renamed`
  - Old only → `Removed`; New only → `Added`
- **Summary generation**: Each `SymbolChange` gets a human-readable `Summary` string built from its deltas. Example: `"Function processOrder: parameter added (string → string, number), return type changed (void → Promise<Result>)"`. Generated deterministically from the deltas, not via templates.

**Behavior** (non-obvious):
- When a file is deleted (`Status == "D"`), all its old definitions become `Removed` changes.
- When a file is added (`Status == "A"`), all its new definitions become `Added` changes.
- For renamed files (`Status == "R"`), old definitions come from parsing old content at `OldPath`; matching proceeds normally against new definitions.
- Definitions with `Label == "Module"` are excluded from diffing (they represent the file itself, not a symbol).

### 3. Breaking Change Classifier
**Files**: `internal/semdiff/breaking.go`, `internal/semdiff/breaking_test.go`

**Key decisions**:
- A change is breaking if **all** of these are true:
  1. The symbol was exported in the old version (`IsExported == true` on old def, or `Added` in a non-test file)
  2. The change kind is `Removed`, `SignatureChanged`, or `Renamed`
  3. For `SignatureChanged`: at least one delta is in `param_types`, `return_type`, or `is_exported` (not just complexity/line count)
- `BodyChanged` on an exported symbol is **never** breaking (internal implementation detail).
- `Added` is **never** breaking (additive change).
- Export removal (`is_exported: true → false`) is breaking regardless of other changes.
- This is a pure function: `ClassifyBreaking(changes []SymbolChange, oldDefs, newDefs map[string]cbm.Definition) []SymbolChange` — it annotates the `IsBreaking` field in-place and returns the subset that are breaking.

### 4. MCP Tool Handler
**Files**: `internal/tools/semantic_diff.go`

**Key decisions**:
- Shares parameters with `detect_changes`: `scope`, `base_branch`, `depth`, `max_impact`, `project`. Adds `include_impact` (bool, default true) to optionally skip the expensive BFS tracing.
- **Pipeline** (sequential — each step feeds the next):
  1. `resolveDetectRepo()` — reuse existing helper
  2. `ParseGitDiffFiles()` + `ParseGitDiffHunks()` — reuse existing
  3. `OldFileContents()` — fetch old versions
  4. For each changed file: `cbm.ExtractFile()` on old content + read new content from disk and extract → produce old/new `[]Definition`
  5. `semdiff.Diff()` per file → `[]SymbolChange`
  6. `semdiff.ClassifyBreaking()` across all changes
  7. If `include_impact`: reuse `mapChangesToSymbols()` + `traceImpact()` from detect_changes
  8. Build `SemanticDiffResult`, return via `s.result()`
- **Reading new file content**: Read from the filesystem at `repoPath/filePath`. For `staged` scope, use `git show :0:<path>` (index version) instead of filesystem to match what's actually staged. For other scopes, filesystem is correct.
- **Error isolation**: Parse failures on individual files are logged and skipped — the tool returns results for all successfully parsed files plus a `warnings` list in the response.
- Registration: `s.registerSemanticDiff()` called from `registerTools()` in `tools.go`.

## File Map

### New Files
| File | Subsystem | Purpose |
|------|-----------|---------|
| `internal/pipeline/gitshow.go` | 1 | Fetch old file contents via `git show` |
| `internal/pipeline/gitshow_test.go` | 1 | Tests with temp git repos |
| `internal/semdiff/types.go` | 2, 3 | Shared types (`SymbolChange`, `FieldDelta`, etc.) |
| `internal/semdiff/differ.go` | 2 | Core diff logic: match + compare definitions |
| `internal/semdiff/differ_test.go` | 2 | Unit tests for matching, delta computation, rename detection |
| `internal/semdiff/breaking.go` | 3 | Breaking change classification |
| `internal/semdiff/breaking_test.go` | 3 | Unit tests for breaking change rules |
| `internal/tools/semantic_diff.go` | 4 | MCP tool handler + registration |

### Modified Files
| File | Change |
|------|--------|
| `internal/tools/tools.go` | Add `s.registerSemanticDiff()` call in `registerTools()` |

## Verification

1. **Signature change detection**: Modify a function's parameters in a test repo, run `semantic_diff` → verify `SignatureChanged` kind with correct `param_types` delta and human-readable summary.
2. **Breaking change flagging**: Remove an exported function, change an exported function's return type, change an unexported function's return type → verify only the first two are flagged as breaking.
3. **Rename detection**: Rename a function (change name, keep body identical) → verify `Renamed` kind with `old_name` populated.
4. **Added/deleted files**: Add a new file with 3 functions, delete a file with 2 functions → verify 3 `Added` + 2 `Removed` changes.
5. **Impact integration**: Change an exported function that has 3 callers in the graph → verify `impact` section shows all 3 with correct risk levels.
6. **Graceful degradation**: Include a binary file and a file with syntax errors in the diff → verify tool returns results for valid files + warnings for failures.
