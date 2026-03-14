package semdiff

import (
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func fn(name, qn, returnType string, params []string, lines int, exported bool) cbm.Definition {
	return cbm.Definition{
		Name:          name,
		QualifiedName: qn,
		Label:         "Function",
		Signature:     name + "(" + strings.Join(params, ", ") + ")",
		ReturnType:    returnType,
		ParamTypes:    params,
		Lines:         lines,
		IsExported:    exported,
	}
}

func cls(name, qn string, lines int, exported bool) cbm.Definition {
	return cbm.Definition{
		Name:          name,
		QualifiedName: qn,
		Label:         "Class",
		Lines:         lines,
		IsExported:    exported,
	}
}

func mod(name, qn string) cbm.Definition {
	return cbm.Definition{
		Name:          name,
		QualifiedName: qn,
		Label:         "Module",
	}
}

func findChange(changes []SymbolChange, name string) (SymbolChange, bool) {
	for _, c := range changes {
		if c.Name == name {
			return c, true
		}
	}
	return SymbolChange{}, false
}

func findDelta(deltas []FieldDelta, field string) (FieldDelta, bool) {
	for _, d := range deltas {
		if d.Field == field {
			return d, true
		}
	}
	return FieldDelta{}, false
}

// ---------------------------------------------------------------------------
// Module label exclusion
// ---------------------------------------------------------------------------

func TestDiff_ModulesExcluded(t *testing.T) {
	old := []cbm.Definition{
		mod("myfile", "myfile"),
		fn("doWork", "myfile.doWork", "void", nil, 10, false),
	}
	nw := []cbm.Definition{
		mod("myfile", "myfile"),
		fn("doWork", "myfile.doWork", "void", nil, 10, false),
	}

	changes := Diff(old, nw, "myfile.go", "M")
	for _, c := range changes {
		if c.Label == "Module" {
			t.Errorf("Module label leaked into diff output: %+v", c)
		}
	}
	// Identical non-module definitions → no changes.
	if len(changes) != 0 {
		t.Errorf("expected 0 changes, got %d: %+v", len(changes), changes)
	}
}

func TestDiff_OnlyModules_NoChanges(t *testing.T) {
	old := []cbm.Definition{mod("a", "a"), mod("b", "b")}
	nw := []cbm.Definition{mod("a", "a"), mod("b", "b")}
	changes := Diff(old, nw, "f.go", "M")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes for module-only defs, got %d", len(changes))
	}
}

// ---------------------------------------------------------------------------
// Added file
// ---------------------------------------------------------------------------

func TestDiff_AddedFile(t *testing.T) {
	nw := []cbm.Definition{
		fn("Alpha", "pkg.Alpha", "string", []string{"int"}, 5, true),
		fn("beta", "pkg.beta", "void", nil, 3, false),
		cls("MyClass", "pkg.MyClass", 20, true),
	}

	changes := Diff(nil, nw, "new.go", "A")

	if len(changes) != 3 {
		t.Fatalf("expected 3 Added changes, got %d", len(changes))
	}
	for _, c := range changes {
		if c.Kind != Added {
			t.Errorf("expected Added, got %s for %s", c.Kind, c.Name)
		}
		if c.FilePath != "new.go" {
			t.Errorf("expected FilePath=new.go, got %s", c.FilePath)
		}
	}
	if _, ok := findChange(changes, "Alpha"); !ok {
		t.Error("Alpha not found in changes")
	}
}

func TestDiff_AddedFile_OldDefsIgnored(t *testing.T) {
	// Even if old defs are provided, status "A" should produce only Added.
	old := []cbm.Definition{fn("ghost", "pkg.ghost", "void", nil, 5, false)}
	nw := []cbm.Definition{fn("real", "pkg.real", "void", nil, 5, false)}

	changes := Diff(old, nw, "f.go", "A")
	if len(changes) != 1 || changes[0].Kind != Added || changes[0].Name != "real" {
		t.Errorf("unexpected changes for status A: %+v", changes)
	}
}

// ---------------------------------------------------------------------------
// Deleted file
// ---------------------------------------------------------------------------

func TestDiff_DeletedFile(t *testing.T) {
	old := []cbm.Definition{
		fn("processOrder", "svc.processOrder", "error", []string{"*Order"}, 30, true),
		fn("validate", "svc.validate", "bool", []string{"string"}, 10, false),
	}

	changes := Diff(old, nil, "svc.go", "D")

	if len(changes) != 2 {
		t.Fatalf("expected 2 Removed changes, got %d", len(changes))
	}
	for _, c := range changes {
		if c.Kind != Removed {
			t.Errorf("expected Removed, got %s for %s", c.Kind, c.Name)
		}
	}
}

func TestDiff_DeletedFile_NewDefsIgnored(t *testing.T) {
	nw := []cbm.Definition{fn("ghost", "pkg.ghost", "void", nil, 5, false)}
	old := []cbm.Definition{fn("real", "pkg.real", "void", nil, 5, false)}

	changes := Diff(old, nw, "f.go", "D")
	if len(changes) != 1 || changes[0].Kind != Removed || changes[0].Name != "real" {
		t.Errorf("unexpected changes for status D: %+v", changes)
	}
}

// ---------------------------------------------------------------------------
// Exact match — no change
// ---------------------------------------------------------------------------

func TestDiff_ExactMatch_NoChange(t *testing.T) {
	def := fn("doWork", "pkg.doWork", "void", []string{"string"}, 10, false)
	changes := Diff([]cbm.Definition{def}, []cbm.Definition{def}, "f.go", "M")
	if len(changes) != 0 {
		t.Errorf("expected no changes for identical definitions, got %d", len(changes))
	}
}

// ---------------------------------------------------------------------------
// Body-only change
// ---------------------------------------------------------------------------

func TestDiff_BodyChanged(t *testing.T) {
	old := fn("doWork", "pkg.doWork", "void", []string{"string"}, 10, false)
	nw := fn("doWork", "pkg.doWork", "void", []string{"string"}, 20, false)
	// Same signature, different line count → BodyChanged.

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Kind != BodyChanged {
		t.Errorf("expected BodyChanged, got %s", c.Kind)
	}
	if c.Name != "doWork" {
		t.Errorf("expected name doWork, got %s", c.Name)
	}
	if !strings.Contains(c.Summary, "doWork") {
		t.Errorf("summary should mention symbol name: %s", c.Summary)
	}
}

func TestDiff_BodyChanged_OnlyComplexity(t *testing.T) {
	old := cbm.Definition{
		Name: "compute", QualifiedName: "pkg.compute", Label: "Function",
		Signature: "compute(x int)", ReturnType: "int", ParamTypes: []string{"int"},
		Lines: 10, Complexity: 3, IsExported: false,
	}
	nw := old
	nw.Complexity = 7

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 || changes[0].Kind != BodyChanged {
		t.Fatalf("expected 1 BodyChanged, got %+v", changes)
	}
	if _, ok := findDelta(changes[0].Deltas, "complexity"); !ok {
		t.Error("expected complexity delta")
	}
}

// ---------------------------------------------------------------------------
// Signature change
// ---------------------------------------------------------------------------

func TestDiff_SignatureChanged_Params(t *testing.T) {
	old := fn("processOrder", "svc.processOrder", "error", []string{"*Order"}, 30, true)
	nw := fn("processOrder", "svc.processOrder", "error", []string{"*Order", "context.Context"}, 30, true)

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "svc.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Kind != SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}
	delta, ok := findDelta(c.Deltas, "param_types")
	if !ok {
		t.Fatal("expected param_types delta")
	}
	if !strings.Contains(delta.New, "context.Context") {
		t.Errorf("param_types delta should mention context.Context: %s", delta.New)
	}
	if !strings.Contains(c.Summary, "processOrder") {
		t.Errorf("summary should mention processOrder: %s", c.Summary)
	}
}

func TestDiff_SignatureChanged_ReturnType(t *testing.T) {
	old := fn("processOrder", "svc.processOrder", "void", []string{"string"}, 10, true)
	nw := fn("processOrder", "svc.processOrder", "Promise<Result>", []string{"string"}, 10, true)

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "svc.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Kind != SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}
	delta, ok := findDelta(c.Deltas, "return_type")
	if !ok {
		t.Fatal("expected return_type delta")
	}
	if delta.Old != "void" || delta.New != "Promise<Result>" {
		t.Errorf("unexpected return_type delta: %+v", delta)
	}
	// Summary should mention the change.
	if !strings.Contains(c.Summary, "void") || !strings.Contains(c.Summary, "Promise<Result>") {
		t.Errorf("summary should mention old and new return type: %s", c.Summary)
	}
}

func TestDiff_SignatureChanged_ExportRemoval(t *testing.T) {
	old := fn("Handler", "pkg.Handler", "void", nil, 5, true)
	nw := fn("Handler", "pkg.Handler", "void", nil, 5, false) // unexported

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "pkg.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	// is_exported change classifies as VisibilityChanged.
	if c.Kind != VisibilityChanged {
		t.Errorf("expected VisibilityChanged (is_exported only), got %s", c.Kind)
	}
	delta, ok := findDelta(c.Deltas, "is_exported")
	if !ok {
		t.Fatal("expected is_exported delta")
	}
	if delta.Old != "true" || delta.New != "false" {
		t.Errorf("unexpected is_exported delta: %+v", delta)
	}
	if !strings.Contains(c.Summary, "export removed") {
		t.Errorf("summary should mention export removed: %s", c.Summary)
	}
}

// ---------------------------------------------------------------------------
// VisibilityChanged
// ---------------------------------------------------------------------------

func TestDiff_VisibilityChanged_ExportGained(t *testing.T) {
	old := fn("helper", "pkg.helper", "void", nil, 5, false)
	nw := fn("helper", "pkg.helper", "void", nil, 5, true) // now exported

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "pkg.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Kind != VisibilityChanged {
		t.Errorf("expected VisibilityChanged, got %s", c.Kind)
	}
	delta, ok := findDelta(c.Deltas, "is_exported")
	if !ok {
		t.Fatal("expected is_exported delta")
	}
	if delta.Old != "false" || delta.New != "true" {
		t.Errorf("unexpected is_exported delta: %+v", delta)
	}
	if !strings.Contains(c.Summary, "symbol exported") {
		t.Errorf("summary should mention 'symbol exported': %s", c.Summary)
	}
}

func TestDiff_VisibilityChanged_PriorityBelowSignatureChanged(t *testing.T) {
	// When both a signature field and is_exported change, SignatureChanged wins.
	old := fn("Handler", "pkg.Handler", "void", nil, 5, true)
	nw := fn("Handler", "pkg.Handler", "int", nil, 5, false) // return type changed + unexported

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "pkg.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Kind != SignatureChanged {
		t.Errorf("expected SignatureChanged (signature fields take priority), got %s", c.Kind)
	}
}

// ---------------------------------------------------------------------------
// Decorators
// ---------------------------------------------------------------------------

func TestDiff_DecoratorsAdded(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true,
	}
	nw := old
	nw.Decorators = []string{"@injectable"}

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "decorators")
	if !ok {
		t.Fatal("expected decorators delta")
	}
	if d.Old != "" || d.New != "@injectable" {
		t.Errorf("unexpected decorators delta: %+v", d)
	}
	if !strings.Contains(changes[0].Summary, "decorators added") {
		t.Errorf("summary should mention 'decorators added': %s", changes[0].Summary)
	}
}

func TestDiff_DecoratorsRemoved(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true, Decorators: []string{"@deprecated"},
	}
	nw := old
	nw.Decorators = nil

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "decorators")
	if !ok {
		t.Fatal("expected decorators delta")
	}
	if d.Old != "@deprecated" || d.New != "" {
		t.Errorf("unexpected decorators delta: %+v", d)
	}
	if !strings.Contains(changes[0].Summary, "decorators removed") {
		t.Errorf("summary should mention 'decorators removed': %s", changes[0].Summary)
	}
}

func TestDiff_DecoratorsChanged_SortedJoin(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true, Decorators: []string{"@deprecated"},
	}
	nw := old
	// Order should not matter — both sides sorted before comparison.
	nw.Decorators = []string{"@injectable", "@deprecated"}

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "decorators")
	if !ok {
		t.Fatal("expected decorators delta")
	}
	if d.Old != "@deprecated" || d.New != "@deprecated, @injectable" {
		t.Errorf("unexpected decorators delta: %+v", d)
	}
	if !strings.Contains(changes[0].Summary, "decorators changed") {
		t.Errorf("summary should mention 'decorators changed': %s", changes[0].Summary)
	}
}

func TestDiff_Decorators_SameSetDifferentOrder_NoChange(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true, Decorators: []string{"@b", "@a"},
	}
	nw := old
	nw.Decorators = []string{"@a", "@b"} // same elements, different order

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 0 {
		t.Errorf("same decorator set in different order should produce no change, got %+v", changes)
	}
}

// ---------------------------------------------------------------------------
// BaseClasses
// ---------------------------------------------------------------------------

func TestDiff_BaseClassesAdded(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true,
	}
	nw := old
	nw.BaseClasses = []string{"BaseService"}

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "base_classes")
	if !ok {
		t.Fatal("expected base_classes delta")
	}
	if d.Old != "" || d.New != "BaseService" {
		t.Errorf("unexpected base_classes delta: %+v", d)
	}
	if !strings.Contains(changes[0].Summary, "base classes added") {
		t.Errorf("summary should mention 'base classes added': %s", changes[0].Summary)
	}
}

func TestDiff_BaseClassesRemoved(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true, BaseClasses: []string{"BaseService"},
	}
	nw := old
	nw.BaseClasses = nil

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "base_classes")
	if !ok {
		t.Fatal("expected base_classes delta")
	}
	if d.Old != "BaseService" || d.New != "" {
		t.Errorf("unexpected base_classes delta: %+v", d)
	}
	if !strings.Contains(changes[0].Summary, "base classes removed") {
		t.Errorf("summary should mention 'base classes removed': %s", changes[0].Summary)
	}
}

func TestDiff_BaseClassesChanged_SortedJoin(t *testing.T) {
	old := cbm.Definition{
		Name: "MyClass", QualifiedName: "pkg.MyClass", Label: "Class",
		Lines: 10, IsExported: true, BaseClasses: []string{"BaseService"},
	}
	nw := old
	nw.BaseClasses = []string{"Loggable", "BaseService"}

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "base_classes")
	if !ok {
		t.Fatal("expected base_classes delta")
	}
	if d.Old != "BaseService" || d.New != "BaseService, Loggable" {
		t.Errorf("unexpected base_classes delta: %+v", d)
	}
	if !strings.Contains(changes[0].Summary, "base classes changed") {
		t.Errorf("summary should mention 'base classes changed': %s", changes[0].Summary)
	}
}

// ---------------------------------------------------------------------------
// Docstring
// ---------------------------------------------------------------------------

func TestDiff_DocstringChanged(t *testing.T) {
	old := cbm.Definition{
		Name: "Process", QualifiedName: "pkg.Process", Label: "Function",
		Lines: 10, IsExported: true, Docstring: "Process handles requests.",
	}
	nw := old
	nw.Docstring = "Process handles all incoming HTTP requests and routes them."

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	d, ok := findDelta(changes[0].Deltas, "docstring")
	if !ok {
		t.Fatal("expected docstring delta")
	}
	// Verify the raw values are stored in the delta.
	if d.Old != "Process handles requests." {
		t.Errorf("unexpected docstring delta Old: %q", d.Old)
	}
	// Summary should say "documentation changed" not dump the content.
	if !strings.Contains(changes[0].Summary, "documentation changed") {
		t.Errorf("summary should mention 'documentation changed': %s", changes[0].Summary)
	}
}

func TestDiff_DocstringUnchanged_NoExtraDelta(t *testing.T) {
	old := cbm.Definition{
		Name: "Process", QualifiedName: "pkg.Process", Label: "Function",
		Lines: 10, IsExported: true, Docstring: "Same doc.",
	}
	nw := old
	nw.Lines = 15 // only lines changed

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if _, ok := findDelta(changes[0].Deltas, "docstring"); ok {
		t.Error("should not emit docstring delta when docstring is unchanged")
	}
}

// ---------------------------------------------------------------------------
// Rename detection
// ---------------------------------------------------------------------------

func TestDiff_RenameDetected(t *testing.T) {
	old := fn("processPayment", "svc.processPayment", "error", []string{"*Payment"}, 25, true)
	nw := fn("handlePayment", "svc.handlePayment", "error", []string{"*Payment"}, 25, true)

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "svc.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %+v", len(changes), changes)
	}
	c := changes[0]
	if c.Kind != Renamed {
		t.Errorf("expected Renamed, got %s", c.Kind)
	}
	if c.Name != "handlePayment" {
		t.Errorf("expected new name handlePayment, got %s", c.Name)
	}
	if c.OldName != "processPayment" {
		t.Errorf("expected OldName=processPayment, got %s", c.OldName)
	}
	if !strings.Contains(c.Summary, "processPayment") {
		t.Errorf("summary should mention old name: %s", c.Summary)
	}
}

func TestDiff_NoRename_MultipleAmbiguousCandidates(t *testing.T) {
	// Two new functions that match the old one — should produce removed + 2 added, not a rename.
	old := fn("doThing", "pkg.doThing", "void", nil, 10, false)
	nw1 := fn("doThingA", "pkg.doThingA", "void", nil, 10, false)
	nw2 := fn("doThingB", "pkg.doThingB", "void", nil, 10, false)

	changes := Diff(
		[]cbm.Definition{old},
		[]cbm.Definition{nw1, nw2},
		"f.go", "M",
	)
	// Expect: doThingA added, doThingB added, doThing removed.
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d: %+v", len(changes), changes)
	}
	removedCount, addedCount := 0, 0
	for _, c := range changes {
		switch c.Kind {
		case Removed:
			removedCount++
		case Added:
			addedCount++
		default:
			t.Errorf("unexpected kind %s for %s", c.Kind, c.Name)
		}
	}
	if removedCount != 1 || addedCount != 2 {
		t.Errorf("expected 1 removed + 2 added, got %d removed + %d added", removedCount, addedCount)
	}
}

func TestDiff_NoRename_DifferentLabel(t *testing.T) {
	oldFn := fn("Worker", "pkg.Worker", "void", nil, 10, true)
	newCls := cbm.Definition{
		Name: "Worker", QualifiedName: "pkg.Worker2", Label: "Class",
		Lines: 10, IsExported: true,
	}

	changes := Diff([]cbm.Definition{oldFn}, []cbm.Definition{newCls}, "f.go", "M")
	kinds := map[ChangeKind]int{}
	for _, c := range changes {
		kinds[c.Kind]++
	}
	if kinds[Renamed] > 0 {
		t.Error("should not rename across different labels")
	}
}

// ---------------------------------------------------------------------------
// Multiple symbols in a file
// ---------------------------------------------------------------------------

func TestDiff_Mixed(t *testing.T) {
	old := []cbm.Definition{
		fn("Alpha", "pkg.Alpha", "string", []string{"int"}, 5, true),    // unchanged
		fn("Beta", "pkg.Beta", "void", []string{"string"}, 10, true),   // signature change
		fn("Gamma", "pkg.Gamma", "bool", []string{"int", "string"}, 8, false), // removed
		mod("pkg", "pkg"),                                               // excluded
	}
	nw := []cbm.Definition{
		fn("Alpha", "pkg.Alpha", "string", []string{"int"}, 5, true),              // unchanged
		fn("Beta", "pkg.Beta", "error", []string{"string"}, 10, true),             // return type changed
		fn("Delta", "pkg.Delta", "void", nil, 6, false),                            // added
		mod("pkg", "pkg"),                                                           // excluded
	}

	changes := Diff(old, nw, "pkg.go", "M")

	// Alpha: no change (identical).
	if _, ok := findChange(changes, "Alpha"); ok {
		t.Error("Alpha should not appear in changes (identical)")
	}

	// Beta: SignatureChanged (return type).
	betaChange, ok := findChange(changes, "Beta")
	if !ok {
		t.Fatal("Beta not found in changes")
	}
	if betaChange.Kind != SignatureChanged {
		t.Errorf("Beta: expected SignatureChanged, got %s", betaChange.Kind)
	}

	// Gamma: Removed.
	gammaChange, ok := findChange(changes, "Gamma")
	if !ok {
		t.Fatal("Gamma not found in changes")
	}
	if gammaChange.Kind != Removed {
		t.Errorf("Gamma: expected Removed, got %s", gammaChange.Kind)
	}

	// Delta: Added.
	deltaChange, ok := findChange(changes, "Delta")
	if !ok {
		t.Fatal("Delta not found in changes")
	}
	if deltaChange.Kind != Added {
		t.Errorf("Delta: expected Added, got %s", deltaChange.Kind)
	}

	// No Module changes.
	for _, c := range changes {
		if c.Label == "Module" {
			t.Errorf("Module leaked into changes: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// File path propagation
// ---------------------------------------------------------------------------

func TestDiff_FilePathInChanges(t *testing.T) {
	old := []cbm.Definition{fn("foo", "pkg.foo", "void", nil, 5, false)}
	nw := []cbm.Definition{fn("bar", "pkg.bar", "void", nil, 5, false)}

	changes := Diff(old, nw, "internal/service/svc.go", "M")
	for _, c := range changes {
		if c.FilePath != "internal/service/svc.go" {
			t.Errorf("expected FilePath=internal/service/svc.go, got %s", c.FilePath)
		}
	}
}

// ---------------------------------------------------------------------------
// Summary generation
// ---------------------------------------------------------------------------

func TestDiff_SummaryBodyChanged(t *testing.T) {
	old := fn("compute", "pkg.compute", "int", []string{"int"}, 5, false)
	nw := fn("compute", "pkg.compute", "int", []string{"int"}, 15, false)

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "f.go", "M")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change")
	}
	if !strings.Contains(changes[0].Summary, "compute") {
		t.Errorf("summary should contain symbol name: %s", changes[0].Summary)
	}
}

func TestDiff_SummaryAdded(t *testing.T) {
	nw := fn("NewFunc", "pkg.NewFunc", "void", nil, 3, true)
	changes := Diff(nil, []cbm.Definition{nw}, "f.go", "A")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change")
	}
	if !strings.Contains(changes[0].Summary, "added") {
		t.Errorf("summary for added should contain 'added': %s", changes[0].Summary)
	}
}

func TestDiff_SummaryRemoved(t *testing.T) {
	old := fn("OldFunc", "pkg.OldFunc", "void", nil, 3, true)
	changes := Diff([]cbm.Definition{old}, nil, "f.go", "D")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change")
	}
	if !strings.Contains(changes[0].Summary, "removed") {
		t.Errorf("summary for removed should contain 'removed': %s", changes[0].Summary)
	}
}

// ---------------------------------------------------------------------------
// Renamed file (Status "R") — behaves like "M" for diffing purposes
// ---------------------------------------------------------------------------

func TestDiff_RenamedFile_ModifiedSymbol(t *testing.T) {
	old := fn("doThing", "pkg.doThing", "void", []string{"string"}, 10, true)
	nw := fn("doThing", "pkg.doThing", "int", []string{"string"}, 10, true) // return type changed

	changes := Diff([]cbm.Definition{old}, []cbm.Definition{nw}, "newpath/pkg.go", "R")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	c := changes[0]
	if c.Kind != SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}
	if c.FilePath != "newpath/pkg.go" {
		t.Errorf("expected FilePath=newpath/pkg.go, got %s", c.FilePath)
	}
}

// ---------------------------------------------------------------------------
// Empty inputs
// ---------------------------------------------------------------------------

func TestDiff_BothEmpty(t *testing.T) {
	changes := Diff(nil, nil, "f.go", "M")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes for empty inputs, got %d", len(changes))
	}
}

func TestDiff_AddedFile_Empty(t *testing.T) {
	changes := Diff(nil, nil, "f.go", "A")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes for empty added file, got %d", len(changes))
	}
}

func TestDiff_DeletedFile_Empty(t *testing.T) {
	changes := Diff(nil, nil, "f.go", "D")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes for empty deleted file, got %d", len(changes))
	}
}

// ---------------------------------------------------------------------------
// isFuzzyMatch edge cases
// ---------------------------------------------------------------------------

func TestIsFuzzyMatch_VeryDifferentLineCounts(t *testing.T) {
	a := fn("a", "pkg.a", "void", []string{"int"}, 5, false)
	b := fn("b", "pkg.b", "string", nil, 500, false) // different return, different params, 100x longer
	if isFuzzyMatch(a, b) {
		t.Error("should not fuzzy-match when line count ratio exceeds 2x")
	}
}

func TestIsFuzzyMatch_ZeroLines(t *testing.T) {
	a := fn("a", "pkg.a", "void", nil, 0, false)
	b := fn("b", "pkg.b", "void", nil, 0, false)
	// Both zero lines + same return + same param count → should match.
	if !isFuzzyMatch(a, b) {
		t.Error("should fuzzy-match when both have zero lines")
	}
}

// ---------------------------------------------------------------------------
// computeDeltas — focused unit tests
// ---------------------------------------------------------------------------

func TestComputeDeltas_NoDifference(t *testing.T) {
	d := fn("f", "pkg.f", "void", []string{"int"}, 5, false)
	deltas := computeDeltas(d, d)
	if len(deltas) != 0 {
		t.Errorf("expected no deltas for identical definitions, got %+v", deltas)
	}
}

func TestComputeDeltas_AllFields(t *testing.T) {
	old := cbm.Definition{
		Name: "f", QualifiedName: "pkg.f", Label: "Function",
		Signature: "f(a int) void", ReturnType: "void",
		ParamTypes: []string{"int"}, Lines: 10, Complexity: 2,
		IsExported: true, IsAbstract: false,
	}
	nw := cbm.Definition{
		Name: "f", QualifiedName: "pkg.f", Label: "Function",
		Signature: "f(a int, b string) error", ReturnType: "error",
		ParamTypes: []string{"int", "string"}, Lines: 20, Complexity: 5,
		IsExported: false, IsAbstract: true,
	}
	deltas := computeDeltas(old, nw)

	fieldSet := map[string]bool{}
	for _, d := range deltas {
		fieldSet[d.Field] = true
	}
	for _, expected := range []string{"signature", "param_types", "return_type", "is_exported", "is_abstract", "complexity", "lines"} {
		if !fieldSet[expected] {
			t.Errorf("expected delta for field %s", expected)
		}
	}
}

// ---------------------------------------------------------------------------
// classifyKind
// ---------------------------------------------------------------------------

func TestClassifyKind_EmptyDeltas(t *testing.T) {
	if kind := classifyKind(nil); kind != BodyChanged {
		t.Errorf("empty deltas should classify as BodyChanged, got %s", kind)
	}
}

func TestClassifyKind_SignatureDelta(t *testing.T) {
	deltas := []FieldDelta{{Field: "return_type", Old: "void", New: "int"}}
	if kind := classifyKind(deltas); kind != SignatureChanged {
		t.Errorf("return_type delta should classify as SignatureChanged, got %s", kind)
	}
}

func TestClassifyKind_ParamTypesDelta(t *testing.T) {
	deltas := []FieldDelta{{Field: "param_types", Old: "int", New: "int, string"}}
	if kind := classifyKind(deltas); kind != SignatureChanged {
		t.Errorf("param_types delta should classify as SignatureChanged, got %s", kind)
	}
}

func TestClassifyKind_SignatureFieldDelta(t *testing.T) {
	deltas := []FieldDelta{{Field: "signature", Old: "f()", New: "f(x int)"}}
	if kind := classifyKind(deltas); kind != SignatureChanged {
		t.Errorf("signature delta should classify as SignatureChanged, got %s", kind)
	}
}

func TestClassifyKind_NonSignatureDeltas(t *testing.T) {
	deltas := []FieldDelta{
		{Field: "complexity", Old: "2", New: "5"},
		{Field: "lines", Old: "10", New: "20"},
	}
	if kind := classifyKind(deltas); kind != BodyChanged {
		t.Errorf("non-signature deltas should classify as BodyChanged, got %s", kind)
	}
}

// ---------------------------------------------------------------------------
// buildSummary
// ---------------------------------------------------------------------------

func TestBuildSummary_EmptyDeltas(t *testing.T) {
	s := buildSummary("Function", "doWork", BodyChanged, nil)
	if !strings.Contains(s, "doWork") || !strings.Contains(s, "body changed") {
		t.Errorf("unexpected summary: %s", s)
	}
}

func TestBuildSummary_ReturnTypeChanged(t *testing.T) {
	deltas := []FieldDelta{{Field: "return_type", Old: "void", New: "int"}}
	s := buildSummary("Function", "doWork", SignatureChanged, deltas)
	if !strings.Contains(s, "void") || !strings.Contains(s, "int") {
		t.Errorf("summary should mention old and new return type: %s", s)
	}
}

func TestBuildSummary_ParamsAdded(t *testing.T) {
	deltas := []FieldDelta{{Field: "param_types", Old: "", New: "int"}}
	s := buildSummary("Function", "f", SignatureChanged, deltas)
	if !strings.Contains(s, "added") {
		t.Errorf("summary should mention 'added': %s", s)
	}
}

func TestBuildSummary_ExportRemoved(t *testing.T) {
	deltas := []FieldDelta{{Field: "is_exported", Old: "true", New: "false"}}
	s := buildSummary("Function", "f", BodyChanged, deltas)
	if !strings.Contains(s, "export removed") {
		t.Errorf("summary should mention 'export removed': %s", s)
	}
}
