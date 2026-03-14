package semdiff

import (
	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
)

// breakingSignatureDeltaFields are the field names that, when changed, constitute
// a breaking signature change. Changes to complexity or line count alone are not breaking.
var breakingSignatureDeltaFields = map[string]bool{
	"param_types":  true,
	"return_type":  true,
	"is_exported":  true,
	"signature":    true,
	"decorators":   true,
	"base_classes": true,
}

// ClassifyBreaking annotates IsBreaking in-place on each SymbolChange and returns
// the subset of changes that are breaking.
//
// A change is breaking when ALL of:
//  1. The symbol was exported in the old version (IsExported == true on old def),
//     or for Added symbols — never breaking regardless of export status.
//  2. The change kind is Removed, SignatureChanged, VisibilityChanged, or Renamed.
//  3. For SignatureChanged specifically: at least one delta touches param_types,
//     return_type, is_exported, signature, decorators, or base_classes (not just
//     complexity/line-count).
//
// BodyChanged is never breaking. Added is never breaking.
// VisibilityChanged is breaking only when the old symbol was exported (export removal).
// Gaining export (unexported → exported) is never breaking (additive).
func ClassifyBreaking(changes []SymbolChange, oldDefs, newDefs map[string]cbm.Definition) []SymbolChange {
	var breaking []SymbolChange

	for i := range changes {
		c := &changes[i]

		switch c.Kind {
		case Added:
			// Additive changes are never breaking.
			c.IsBreaking = false

		case BodyChanged:
			// Implementation-only change; callers are unaffected.
			c.IsBreaking = false

		case Removed:
			// A removed exported symbol is always breaking.
			if wasExported(c, oldDefs) {
				c.IsBreaking = true
			}

		case Renamed:
			// Renaming an exported symbol breaks callers referencing the old name.
			if wasExported(c, oldDefs) {
				c.IsBreaking = true
			}

		case SignatureChanged:
			// Only breaking when the old symbol was exported AND at least one
			// structurally significant field changed.
			if wasExported(c, oldDefs) && hasBreakingDelta(c.Deltas) {
				c.IsBreaking = true
			}

		case VisibilityChanged:
			// Breaking only when the symbol was previously exported and is now unexported.
			// Gaining export (unexported → exported) is additive and never breaking.
			if wasExported(c, oldDefs) {
				c.IsBreaking = true
			}
		}

		if c.IsBreaking {
			breaking = append(breaking, *c)
		}
	}

	return breaking
}

// wasExported reports whether the symbol represented by c was exported in the old
// version. It looks up the old definition by QualifiedName. For Removed and Renamed
// symbols the old definition must exist; for other kinds it falls back to the delta
// (is_exported field) if the definition is absent.
func wasExported(c *SymbolChange, oldDefs map[string]cbm.Definition) bool {
	if def, ok := oldDefs[c.QualifiedName]; ok {
		return def.IsExported
	}

	// For renames the old qualified name differs from the current one; check
	// OldName as a fallback key.
	if c.OldName != "" {
		if def, ok := oldDefs[c.OldName]; ok {
			return def.IsExported
		}
	}

	// Last resort: inspect deltas for an is_exported change and use the old value.
	for _, d := range c.Deltas {
		if d.Field == "is_exported" {
			return d.Old == "true"
		}
	}

	// No information available — conservative: treat as non-exported.
	return false
}

// hasBreakingDelta reports whether any delta in the slice touches a field that
// constitutes a breaking change (as opposed to a cosmetic/complexity change).
func hasBreakingDelta(deltas []FieldDelta) bool {
	for _, d := range deltas {
		if breakingSignatureDeltaFields[d.Field] {
			return true
		}
	}
	return false
}
