package semdiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
)

// Diff compares two slices of definitions for a single file and returns the set
// of symbol changes. oldDefs and newDefs may both be nil (e.g. added/deleted
// files — the caller passes one side as nil and the other as the full list).
//
// filePath is the current (new) path of the file. fileStatus is one of "M",
// "A", "D", or "R".
//
// Definitions whose Label is "Module" are excluded from diffing.
func Diff(oldDefs, newDefs []cbm.Definition, filePath, fileStatus string) []SymbolChange {
	old := filterModules(oldDefs)
	nw := filterModules(newDefs)

	// Fast paths for added / deleted files.
	if fileStatus == "A" {
		return addedAll(nw, filePath)
	}
	if fileStatus == "D" {
		return removedAll(old, filePath)
	}

	// Build lookup maps keyed by QualifiedName.
	oldByQN := make(map[string]cbm.Definition, len(old))
	for _, d := range old {
		oldByQN[d.QualifiedName] = d
	}
	newByQN := make(map[string]cbm.Definition, len(nw))
	for _, d := range nw {
		newByQN[d.QualifiedName] = d
	}

	var changes []SymbolChange

	// Pass 1: exact match on QualifiedName.
	matchedOld := make(map[string]bool)
	matchedNew := make(map[string]bool)

	for qn, oldDef := range oldByQN {
		if newDef, ok := newByQN[qn]; ok {
			matchedOld[qn] = true
			matchedNew[qn] = true
			if sc, changed := compareDefinitions(oldDef, newDef, filePath); changed {
				changes = append(changes, sc)
			}
		}
	}

	// Collect unmatched definitions.
	var unmatchedOld []cbm.Definition
	for _, d := range old {
		if !matchedOld[d.QualifiedName] {
			unmatchedOld = append(unmatchedOld, d)
		}
	}
	var unmatchedNew []cbm.Definition
	for _, d := range nw {
		if !matchedNew[d.QualifiedName] {
			unmatchedNew = append(unmatchedNew, d)
		}
	}

	// Pass 2: fuzzy match for rename detection.
	// Group unmatched by label to reduce the search space.
	unmatchedOldByLabel := groupByLabel(unmatchedOld)
	unmatchedNewByLabel := groupByLabel(unmatchedNew)

	renamedOld := make(map[string]bool)
	renamedNew := make(map[string]bool)

	for label, oldGroup := range unmatchedOldByLabel {
		newGroup, ok := unmatchedNewByLabel[label]
		if !ok {
			continue
		}
		for _, od := range oldGroup {
			if renamedOld[od.QualifiedName] {
				continue
			}
			var candidates []cbm.Definition
			for _, nd := range newGroup {
				if !renamedNew[nd.QualifiedName] && isFuzzyMatch(od, nd) {
					candidates = append(candidates, nd)
				}
			}
			// Deliberately conservative: only accept single-candidate renames.
			if len(candidates) == 1 {
				nd := candidates[0]
				renamedOld[od.QualifiedName] = true
				renamedNew[nd.QualifiedName] = true
				changes = append(changes, buildRenameChange(od, nd, filePath))
			}
		}
	}

	// Pass 3: remaining unmatched → Removed / Added.
	for _, d := range unmatchedOld {
		if !renamedOld[d.QualifiedName] {
			changes = append(changes, removedChange(d, filePath))
		}
	}
	for _, d := range unmatchedNew {
		if !renamedNew[d.QualifiedName] {
			changes = append(changes, addedChange(d, filePath))
		}
	}

	return changes
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func filterModules(defs []cbm.Definition) []cbm.Definition {
	var out []cbm.Definition
	for _, d := range defs {
		if d.Label != "Module" {
			out = append(out, d)
		}
	}
	return out
}

func addedAll(defs []cbm.Definition, filePath string) []SymbolChange {
	out := make([]SymbolChange, 0, len(defs))
	for _, d := range defs {
		out = append(out, addedChange(d, filePath))
	}
	return out
}

func removedAll(defs []cbm.Definition, filePath string) []SymbolChange {
	out := make([]SymbolChange, 0, len(defs))
	for _, d := range defs {
		out = append(out, removedChange(d, filePath))
	}
	return out
}

func addedChange(d cbm.Definition, filePath string) SymbolChange {
	return SymbolChange{
		Name:          d.Name,
		QualifiedName: d.QualifiedName,
		Label:         d.Label,
		FilePath:      filePath,
		Kind:          Added,
		Summary:       fmt.Sprintf("%s %s: added", d.Label, d.Name),
	}
}

func removedChange(d cbm.Definition, filePath string) SymbolChange {
	return SymbolChange{
		Name:          d.Name,
		QualifiedName: d.QualifiedName,
		Label:         d.Label,
		FilePath:      filePath,
		Kind:          Removed,
		Summary:       fmt.Sprintf("%s %s: removed", d.Label, d.Name),
	}
}

// compareDefinitions returns a SymbolChange for a matched old/new pair.
// changed is false when the definitions are structurally identical (no deltas
// and same line count — treat as no-op).
func compareDefinitions(od, nd cbm.Definition, filePath string) (SymbolChange, bool) {
	deltas := computeDeltas(od, nd)

	if len(deltas) == 0 && od.Lines == nd.Lines {
		// Identical — no change to report.
		return SymbolChange{}, false
	}

	kind := classifyKind(deltas)

	return SymbolChange{
		Name:          nd.Name,
		QualifiedName: nd.QualifiedName,
		Label:         nd.Label,
		FilePath:      filePath,
		Kind:          kind,
		Deltas:        deltas,
		Summary:       buildSummary(nd.Label, nd.Name, kind, deltas),
	}, true
}

// computeDeltas returns the list of field-level changes between two definitions.
func computeDeltas(od, nd cbm.Definition) []FieldDelta {
	var deltas []FieldDelta

	if od.Signature != nd.Signature {
		deltas = append(deltas, FieldDelta{
			Field: "signature",
			Old:   od.Signature,
			New:   nd.Signature,
		})
	}

	oldParams := joinParams(od.ParamTypes)
	newParams := joinParams(nd.ParamTypes)
	if oldParams != newParams {
		deltas = append(deltas, FieldDelta{
			Field: "param_types",
			Old:   oldParams,
			New:   newParams,
		})
	}

	if od.ReturnType != nd.ReturnType {
		deltas = append(deltas, FieldDelta{
			Field: "return_type",
			Old:   od.ReturnType,
			New:   nd.ReturnType,
		})
	}

	if od.IsExported != nd.IsExported {
		deltas = append(deltas, FieldDelta{
			Field: "is_exported",
			Old:   boolStr(od.IsExported),
			New:   boolStr(nd.IsExported),
		})
	}

	if od.IsAbstract != nd.IsAbstract {
		deltas = append(deltas, FieldDelta{
			Field: "is_abstract",
			Old:   boolStr(od.IsAbstract),
			New:   boolStr(nd.IsAbstract),
		})
	}

	if od.Complexity != nd.Complexity {
		deltas = append(deltas, FieldDelta{
			Field: "complexity",
			Old:   fmt.Sprintf("%d", od.Complexity),
			New:   fmt.Sprintf("%d", nd.Complexity),
		})
	}

	if od.Lines != nd.Lines {
		deltas = append(deltas, FieldDelta{
			Field: "lines",
			Old:   fmt.Sprintf("%d", od.Lines),
			New:   fmt.Sprintf("%d", nd.Lines),
		})
	}

	oldDecorators := sortedJoin(od.Decorators)
	newDecorators := sortedJoin(nd.Decorators)
	if oldDecorators != newDecorators {
		deltas = append(deltas, FieldDelta{
			Field: "decorators",
			Old:   oldDecorators,
			New:   newDecorators,
		})
	}

	oldBaseClasses := sortedJoin(od.BaseClasses)
	newBaseClasses := sortedJoin(nd.BaseClasses)
	if oldBaseClasses != newBaseClasses {
		deltas = append(deltas, FieldDelta{
			Field: "base_classes",
			Old:   oldBaseClasses,
			New:   newBaseClasses,
		})
	}

	if od.Docstring != nd.Docstring {
		deltas = append(deltas, FieldDelta{
			Field: "docstring",
			Old:   od.Docstring,
			New:   nd.Docstring,
		})
	}

	return deltas
}

// classifyKind determines the ChangeKind from a set of deltas.
// Rules (highest priority first):
//  1. Any delta in signature, param_types, or return_type → SignatureChanged
//  2. Any delta in is_exported → VisibilityChanged
//  3. No deltas or any other delta → BodyChanged (complexity, lines, is_abstract, etc.)
func classifyKind(deltas []FieldDelta) ChangeKind {
	hasVisibility := false
	for _, d := range deltas {
		switch d.Field {
		case "signature", "param_types", "return_type":
			return SignatureChanged
		case "is_exported":
			hasVisibility = true
		}
	}
	if hasVisibility {
		return VisibilityChanged
	}
	return BodyChanged
}

// isFuzzyMatch returns true when two definitions of the same label are
// considered a rename candidate. The heuristic is deliberately conservative:
// at least 2 out of 3 structural attributes must match (param count, return
// type, similar line count within ±50%).
func isFuzzyMatch(od, nd cbm.Definition) bool {
	score := 0

	// Same param count.
	if len(od.ParamTypes) == len(nd.ParamTypes) {
		score++
	}
	// Same return type.
	if od.ReturnType == nd.ReturnType {
		score++
	}
	// Similar line count (within 50%).
	if od.Lines > 0 && nd.Lines > 0 {
		ratio := float64(nd.Lines) / float64(od.Lines)
		if ratio >= 0.5 && ratio <= 2.0 {
			score++
		}
	} else if od.Lines == nd.Lines {
		// Both zero.
		score++
	}

	return score >= 2
}

func buildRenameChange(od, nd cbm.Definition, filePath string) SymbolChange {
	deltas := computeDeltas(od, nd)
	return SymbolChange{
		Name:          nd.Name,
		QualifiedName: nd.QualifiedName,
		Label:         nd.Label,
		FilePath:      filePath,
		Kind:          Renamed,
		OldName:       od.Name,
		Deltas:        deltas,
		Summary:       fmt.Sprintf("%s %s: renamed from %s", nd.Label, nd.Name, od.Name),
	}
}

// groupByLabel groups definitions by their Label field.
func groupByLabel(defs []cbm.Definition) map[string][]cbm.Definition {
	m := make(map[string][]cbm.Definition)
	for _, d := range defs {
		m[d.Label] = append(m[d.Label], d)
	}
	return m
}

// ---------------------------------------------------------------------------
// Summary generation
// ---------------------------------------------------------------------------

// buildSummary constructs a human-readable one-liner for a symbol change.
func buildSummary(label, name string, kind ChangeKind, deltas []FieldDelta) string {
	if len(deltas) == 0 {
		return fmt.Sprintf("%s %s: body changed", label, name)
	}

	parts := make([]string, 0, len(deltas))
	for _, d := range deltas {
		parts = append(parts, describeDelta(d))
	}
	return fmt.Sprintf("%s %s: %s", label, name, strings.Join(parts, ", "))
}

// describeDelta converts a single FieldDelta to a human-readable phrase.
func describeDelta(d FieldDelta) string {
	switch d.Field {
	case "param_types":
		if d.Old == "" && d.New != "" {
			return fmt.Sprintf("parameters added (%s)", d.New)
		}
		if d.Old != "" && d.New == "" {
			return fmt.Sprintf("parameters removed (%s)", d.Old)
		}
		return fmt.Sprintf("parameter list changed (%s → %s)", d.Old, d.New)
	case "return_type":
		if d.Old == "" {
			return fmt.Sprintf("return type added (%s)", d.New)
		}
		if d.New == "" {
			return fmt.Sprintf("return type removed (%s)", d.Old)
		}
		return fmt.Sprintf("return type changed (%s → %s)", d.Old, d.New)
	case "signature":
		return fmt.Sprintf("signature changed (%s → %s)", d.Old, d.New)
	case "is_exported":
		if d.New == "true" {
			return "symbol exported"
		}
		return "export removed"
	case "is_abstract":
		if d.New == "true" {
			return "made abstract"
		}
		return "abstraction removed"
	case "complexity":
		return fmt.Sprintf("complexity changed (%s → %s)", d.Old, d.New)
	case "lines":
		return fmt.Sprintf("line count changed (%s → %s)", d.Old, d.New)
	case "decorators":
		if d.Old == "" && d.New != "" {
			return fmt.Sprintf("decorators added (%s)", d.New)
		}
		if d.Old != "" && d.New == "" {
			return fmt.Sprintf("decorators removed (%s)", d.Old)
		}
		return fmt.Sprintf("decorators changed (%s → %s)", d.Old, d.New)
	case "base_classes":
		if d.Old == "" && d.New != "" {
			return fmt.Sprintf("base classes added (%s)", d.New)
		}
		if d.Old != "" && d.New == "" {
			return fmt.Sprintf("base classes removed (%s)", d.Old)
		}
		return fmt.Sprintf("base classes changed (%s → %s)", d.Old, d.New)
	case "docstring":
		return "documentation changed"
	default:
		return fmt.Sprintf("%s changed (%s → %s)", d.Field, d.Old, d.New)
	}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func joinParams(params []string) string {
	return strings.Join(params, ", ")
}

// sortedJoin sorts a slice of strings and joins them with ", ".
// Returns an empty string for nil or empty slices.
func sortedJoin(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	cp := make([]string, len(ss))
	copy(cp, ss)
	sort.Strings(cp)
	return strings.Join(cp, ", ")
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
