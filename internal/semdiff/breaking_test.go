package semdiff

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
)

// helpers

func exported(name, qname string) cbm.Definition {
	return cbm.Definition{Name: name, QualifiedName: qname, IsExported: true}
}

func unexported(name, qname string) cbm.Definition {
	return cbm.Definition{Name: name, QualifiedName: qname, IsExported: false}
}

func change(qname string, kind ChangeKind, deltas ...FieldDelta) SymbolChange {
	return SymbolChange{
		Name:          qname,
		QualifiedName: qname,
		Kind:          kind,
		Deltas:        deltas,
	}
}

func delta(field, old, new_ string) FieldDelta {
	return FieldDelta{Field: field, Old: old, New: new_}
}

// ---- ClassifyBreaking tests ----

func TestClassifyBreaking_RemovedExported(t *testing.T) {
	changes := []SymbolChange{
		change("pkg.ProcessOrder", Removed),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.ProcessOrder": exported("ProcessOrder", "pkg.ProcessOrder"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("change should be marked IsBreaking=true")
	}
	if breaking[0].QualifiedName != "pkg.ProcessOrder" {
		t.Errorf("unexpected qualified name %q", breaking[0].QualifiedName)
	}
}

func TestClassifyBreaking_RemovedUnexported_NotBreaking(t *testing.T) {
	changes := []SymbolChange{
		change("pkg.helper", Removed),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.helper": unexported("helper", "pkg.helper"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for unexported removal, got %d", len(breaking))
	}
	if changes[0].IsBreaking {
		t.Error("unexported removal should not be IsBreaking")
	}
}

func TestClassifyBreaking_SignatureChanged_ParamTypes(t *testing.T) {
	changes := []SymbolChange{
		change("pkg.Send", SignatureChanged,
			delta("param_types", "[string]", "[string, number]"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.Send": exported("Send", "pkg.Send"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("exported param_types change should be breaking")
	}
}

func TestClassifyBreaking_SignatureChanged_ReturnType(t *testing.T) {
	changes := []SymbolChange{
		change("pkg.Fetch", SignatureChanged,
			delta("return_type", "void", "Promise<Result>"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.Fetch": exported("Fetch", "pkg.Fetch"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change, got %d", len(breaking))
	}
}

func TestClassifyBreaking_SignatureChanged_ComplexityOnly_NotBreaking(t *testing.T) {
	// Complexity or line-count delta on an exported function is NOT breaking.
	changes := []SymbolChange{
		change("pkg.Compute", SignatureChanged,
			delta("complexity", "3", "7"),
			delta("lines", "10", "20"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.Compute": exported("Compute", "pkg.Compute"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for complexity-only delta, got %d", len(breaking))
	}
	if changes[0].IsBreaking {
		t.Error("complexity/lines delta should not be IsBreaking")
	}
}

func TestClassifyBreaking_SignatureChanged_UnexportedWithParamChange_NotBreaking(t *testing.T) {
	changes := []SymbolChange{
		change("pkg.internalFn", SignatureChanged,
			delta("param_types", "[string]", "[string, int]"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.internalFn": unexported("internalFn", "pkg.internalFn"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for unexported signature change, got %d", len(breaking))
	}
}

func TestClassifyBreaking_BodyChanged_NotBreaking(t *testing.T) {
	// Body-only change on an exported function is never breaking.
	changes := []SymbolChange{
		change("pkg.PublicFn", BodyChanged),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.PublicFn": exported("PublicFn", "pkg.PublicFn"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for body-only change, got %d", len(breaking))
	}
	if changes[0].IsBreaking {
		t.Error("BodyChanged should never be IsBreaking")
	}
}

func TestClassifyBreaking_Added_NeverBreaking(t *testing.T) {
	changes := []SymbolChange{
		change("pkg.NewFunction", Added),
	}
	// Added symbols have no old definition.
	oldDefs := map[string]cbm.Definition{}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for added symbol, got %d", len(breaking))
	}
	if changes[0].IsBreaking {
		t.Error("Added should never be IsBreaking")
	}
}

func TestClassifyBreaking_ExportRemoval_Breaking(t *testing.T) {
	// is_exported delta from true → false on a SignatureChanged symbol is breaking.
	changes := []SymbolChange{
		change("pkg.WasPublic", SignatureChanged,
			delta("is_exported", "true", "false"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.WasPublic": exported("WasPublic", "pkg.WasPublic"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change for export removal, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("export removal should be IsBreaking")
	}
}

func TestClassifyBreaking_RenamedExported_Breaking(t *testing.T) {
	changes := []SymbolChange{{
		Name:          "pkg.ProcessOrderV2",
		QualifiedName: "pkg.ProcessOrderV2",
		OldName:       "pkg.ProcessOrder",
		Kind:          Renamed,
	}}
	oldDefs := map[string]cbm.Definition{
		"pkg.ProcessOrder": exported("ProcessOrder", "pkg.ProcessOrder"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change for exported rename, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("renamed exported symbol should be IsBreaking")
	}
}

func TestClassifyBreaking_RenamedUnexported_NotBreaking(t *testing.T) {
	changes := []SymbolChange{{
		Name:          "pkg.helperV2",
		QualifiedName: "pkg.helperV2",
		OldName:       "pkg.helper",
		Kind:          Renamed,
	}}
	oldDefs := map[string]cbm.Definition{
		"pkg.helper": unexported("helper", "pkg.helper"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for unexported rename, got %d", len(breaking))
	}
}

func TestClassifyBreaking_MixedChanges(t *testing.T) {
	// Multiple changes: only the exported removal and exported signature change are breaking.
	changes := []SymbolChange{
		change("pkg.PublicRemoved", Removed),
		change("pkg.privateRemoved", Removed),
		change("pkg.PublicSigChange", SignatureChanged, delta("return_type", "int", "string")),
		change("pkg.PublicBodyOnly", BodyChanged),
		change("pkg.NewPublic", Added),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.PublicRemoved":   exported("PublicRemoved", "pkg.PublicRemoved"),
		"pkg.privateRemoved":  unexported("privateRemoved", "pkg.privateRemoved"),
		"pkg.PublicSigChange": exported("PublicSigChange", "pkg.PublicSigChange"),
		"pkg.PublicBodyOnly":  exported("PublicBodyOnly", "pkg.PublicBodyOnly"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 2 {
		t.Fatalf("expected 2 breaking changes, got %d", len(breaking))
	}

	breakingNames := map[string]bool{}
	for _, b := range breaking {
		breakingNames[b.QualifiedName] = true
	}

	if !breakingNames["pkg.PublicRemoved"] {
		t.Error("pkg.PublicRemoved should be breaking")
	}
	if !breakingNames["pkg.PublicSigChange"] {
		t.Error("pkg.PublicSigChange should be breaking")
	}
	if breakingNames["pkg.privateRemoved"] {
		t.Error("pkg.privateRemoved should NOT be breaking")
	}
	if breakingNames["pkg.PublicBodyOnly"] {
		t.Error("pkg.PublicBodyOnly (BodyChanged) should NOT be breaking")
	}
	if breakingNames["pkg.NewPublic"] {
		t.Error("pkg.NewPublic (Added) should NOT be breaking")
	}
}

func TestClassifyBreaking_MutatesIsBreakingInPlace(t *testing.T) {
	// Verify that the original slice is mutated, not just the returned subset.
	changes := []SymbolChange{
		change("pkg.Exported", Removed),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.Exported": exported("Exported", "pkg.Exported"),
	}

	ClassifyBreaking(changes, oldDefs, nil)

	if !changes[0].IsBreaking {
		t.Error("ClassifyBreaking must mutate IsBreaking in-place on the input slice")
	}
}

func TestClassifyBreaking_OldDefMissing_FallsBackToIsExportedDelta(t *testing.T) {
	// When the old definition is not present in oldDefs (e.g., called with an
	// incomplete map), the is_exported delta is used as a fallback.
	changes := []SymbolChange{
		change("pkg.Mystery", SignatureChanged,
			delta("is_exported", "true", "false"),
			delta("return_type", "int", "string"),
		),
	}
	oldDefs := map[string]cbm.Definition{} // intentionally empty

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change via delta fallback, got %d", len(breaking))
	}
}

func TestClassifyBreaking_SignatureFieldDelta_Breaking(t *testing.T) {
	// A delta on the raw "signature" field (e.g., different receiver) is breaking.
	changes := []SymbolChange{
		change("pkg.Method", SignatureChanged,
			delta("signature", "func (r *Receiver) Method()", "func (r Receiver) Method()"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.Method": exported("Method", "pkg.Method"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change for signature field delta, got %d", len(breaking))
	}
}

// ---------------------------------------------------------------------------
// VisibilityChanged
// ---------------------------------------------------------------------------

func TestClassifyBreaking_VisibilityChanged_ExportRemoval_Breaking(t *testing.T) {
	// Exported symbol becomes unexported → breaking.
	changes := []SymbolChange{
		change("pkg.WasPublic", VisibilityChanged,
			delta("is_exported", "true", "false"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.WasPublic": exported("WasPublic", "pkg.WasPublic"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change for export removal, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("VisibilityChanged export removal should be IsBreaking")
	}
}

func TestClassifyBreaking_VisibilityChanged_ExportGained_NotBreaking(t *testing.T) {
	// Unexported symbol becomes exported → additive, not breaking.
	changes := []SymbolChange{
		change("pkg.nowPublic", VisibilityChanged,
			delta("is_exported", "false", "true"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.nowPublic": unexported("nowPublic", "pkg.nowPublic"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for gaining export, got %d", len(breaking))
	}
	if changes[0].IsBreaking {
		t.Error("gaining export (unexported → exported) should NOT be IsBreaking")
	}
}

// ---------------------------------------------------------------------------
// Decorators
// ---------------------------------------------------------------------------

func TestClassifyBreaking_DecoratorsChanged_Exported_Breaking(t *testing.T) {
	// Changing decorators on an exported symbol is breaking.
	changes := []SymbolChange{
		change("pkg.MyClass", SignatureChanged,
			delta("decorators", "@deprecated", "@deprecated, @injectable"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.MyClass": exported("MyClass", "pkg.MyClass"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change for decorator change on exported symbol, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("decorator change on exported symbol should be IsBreaking")
	}
}

func TestClassifyBreaking_DecoratorsChanged_Unexported_NotBreaking(t *testing.T) {
	// Changing decorators on an unexported symbol is not breaking.
	changes := []SymbolChange{
		change("pkg.internal", SignatureChanged,
			delta("decorators", "@deprecated", ""),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.internal": unexported("internal", "pkg.internal"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for decorator change on unexported symbol, got %d", len(breaking))
	}
}

// ---------------------------------------------------------------------------
// BaseClasses
// ---------------------------------------------------------------------------

func TestClassifyBreaking_BaseClassesChanged_Exported_Breaking(t *testing.T) {
	// Changing base classes on an exported symbol is breaking.
	changes := []SymbolChange{
		change("pkg.MyClass", SignatureChanged,
			delta("base_classes", "BaseService", "BaseService, Loggable"),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.MyClass": exported("MyClass", "pkg.MyClass"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 1 {
		t.Fatalf("expected 1 breaking change for base_classes change on exported symbol, got %d", len(breaking))
	}
	if !changes[0].IsBreaking {
		t.Error("base_classes change on exported symbol should be IsBreaking")
	}
}

func TestClassifyBreaking_BaseClassesChanged_Unexported_NotBreaking(t *testing.T) {
	// Changing base classes on an unexported symbol is not breaking.
	changes := []SymbolChange{
		change("pkg.internalClass", SignatureChanged,
			delta("base_classes", "Base", ""),
		),
	}
	oldDefs := map[string]cbm.Definition{
		"pkg.internalClass": unexported("internalClass", "pkg.internalClass"),
	}

	breaking := ClassifyBreaking(changes, oldDefs, nil)

	if len(breaking) != 0 {
		t.Fatalf("expected 0 breaking changes for base_classes change on unexported symbol, got %d", len(breaking))
	}
}
