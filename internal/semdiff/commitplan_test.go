package semdiff

import (
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sc(name, qn, filePath string, kind ChangeKind) SymbolChange {
	return SymbolChange{
		Name:          name,
		QualifiedName: qn,
		Label:         "Function",
		FilePath:      filePath,
		Kind:          kind,
		Summary:       kind.String() + " " + name,
	}
}

func (k ChangeKind) String() string { return string(k) }

func scBreaking(name, qn, filePath string, kind ChangeKind) SymbolChange {
	c := sc(name, qn, filePath, kind)
	c.IsBreaking = true
	return c
}

func scRenamed(name, oldName, qn, filePath string) SymbolChange {
	return SymbolChange{
		Name:          name,
		QualifiedName: qn,
		OldName:       oldName,
		Label:         "Function",
		FilePath:      filePath,
		Kind:          Renamed,
		Summary:       "renamed " + name,
	}
}

func scSig(name, qn, filePath string) SymbolChange {
	return SymbolChange{
		Name:          name,
		QualifiedName: qn,
		Label:         "Function",
		FilePath:      filePath,
		Kind:          SignatureChanged,
		Deltas: []FieldDelta{
			{Field: "param_types", Old: "string", New: "string, int"},
		},
		Summary: "signature changed " + name,
	}
}

func fsum(path string, changes ...SymbolChange) FileChangeSummary {
	return FileChangeSummary{Path: path, Status: "M", Changes: changes}
}

func findGroup(groups []CommitGroup, file string) (CommitGroup, bool) {
	for _, g := range groups {
		for _, f := range g.Files {
			if f == file {
				return g, true
			}
		}
	}
	return CommitGroup{}, false
}

// ---------------------------------------------------------------------------
// Empty input
// ---------------------------------------------------------------------------

func TestPlanCommits_Empty(t *testing.T) {
	plan := PlanCommits(nil, nil)
	if len(plan.Commits) != 0 {
		t.Errorf("expected 0 commits for empty input, got %d", len(plan.Commits))
	}
	if plan.Stats.TotalCommits != 0 {
		t.Errorf("expected 0 total commits stat, got %d", plan.Stats.TotalCommits)
	}
}

func TestPlanCommits_EmptyChangesInFile(t *testing.T) {
	// File summaries with no symbol changes → no commits.
	plan := PlanCommits([]FileChangeSummary{
		{Path: "internal/foo/foo.go", Status: "M"},
	}, nil)
	if len(plan.Commits) != 0 {
		t.Errorf("expected 0 commits when files have no symbol changes, got %d", len(plan.Commits))
	}
}

// ---------------------------------------------------------------------------
// Single file → single commit
// ---------------------------------------------------------------------------

func TestPlanCommits_SingleFile(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pipeline/gitdiff.go",
			sc("ParseGitDiff", "pipeline.ParseGitDiff", "internal/pipeline/gitdiff.go", Added),
			sc("parseLine", "pipeline.parseLine", "internal/pipeline/gitdiff.go", BodyChanged),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit for single file, got %d", len(plan.Commits))
	}
	g := plan.Commits[0]
	if len(g.Files) != 1 || g.Files[0] != "internal/pipeline/gitdiff.go" {
		t.Errorf("unexpected files: %v", g.Files)
	}
	if g.Scope != "pipeline" {
		t.Errorf("expected scope=pipeline, got %q", g.Scope)
	}
	if len(g.Summaries) != 2 {
		t.Errorf("expected 2 summaries, got %d", len(g.Summaries))
	}
}

// ---------------------------------------------------------------------------
// Multiple uncoupled files → multiple commits
// ---------------------------------------------------------------------------

func TestPlanCommits_UncoupledFiles(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/tools/detect.go",
			sc("DetectChanges", "tools.DetectChanges", "internal/tools/detect.go", BodyChanged),
		),
		fsum("internal/store/store.go",
			sc("OpenStore", "store.OpenStore", "internal/store/store.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 2 {
		t.Fatalf("expected 2 commits for uncoupled files, got %d: %+v", len(plan.Commits), plan.Commits)
	}

	_, hasTools := findGroup(plan.Commits, "internal/tools/detect.go")
	_, hasStore := findGroup(plan.Commits, "internal/store/store.go")
	if !hasTools {
		t.Error("expected a commit group for tools/detect.go")
	}
	if !hasStore {
		t.Error("expected a commit group for store/store.go")
	}
}

// ---------------------------------------------------------------------------
// Coupled via edge → merged into one commit
// ---------------------------------------------------------------------------

func TestPlanCommits_CoupledViaEdge(t *testing.T) {
	plan := PlanCommits(
		[]FileChangeSummary{
			fsum("internal/pipeline/gitdiff.go",
				scSig("ParseGitDiffFiles", "pipeline.ParseGitDiffFiles", "internal/pipeline/gitdiff.go"),
			),
			fsum("internal/tools/detect_changes.go",
				sc("DetectChanges", "tools.DetectChanges", "internal/tools/detect_changes.go", BodyChanged),
			),
		},
		[]CouplingEdge{
			{FromQN: "tools.DetectChanges", ToQN: "pipeline.ParseGitDiffFiles", Type: "CALLS"},
		},
	)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit (coupled via edge), got %d: %+v", len(plan.Commits), plan.Commits)
	}
	g := plan.Commits[0]
	if len(g.Files) != 2 {
		t.Errorf("expected 2 files in merged commit, got %d: %v", len(g.Files), g.Files)
	}
	if !strings.Contains(g.Reason, "coupled") {
		t.Errorf("reason should mention 'coupled', got %q", g.Reason)
	}
}

// ---------------------------------------------------------------------------
// Test-source coupling → merged
// ---------------------------------------------------------------------------

func TestPlanCommits_TestSourceCoupling_Go(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/semdiff/differ.go",
			sc("Diff", "semdiff.Diff", "internal/semdiff/differ.go", SignatureChanged),
		),
		fsum("internal/semdiff/differ_test.go",
			sc("TestDiff_Added", "semdiff.TestDiff_Added", "internal/semdiff/differ_test.go", Added),
			sc("TestDiff_Removed", "semdiff.TestDiff_Removed", "internal/semdiff/differ_test.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit (test+source merged), got %d: %+v", len(plan.Commits), plan.Commits)
	}
	g := plan.Commits[0]
	if g.TestCount != 2 {
		t.Errorf("expected TestCount=2, got %d", g.TestCount)
	}
	// Test changes should NOT appear in Summaries.
	for _, s := range g.Summaries {
		if strings.Contains(s, "TestDiff") {
			t.Errorf("test change summary leaked into Summaries: %q", s)
		}
	}
	if !strings.Contains(g.Reason, "test") {
		t.Errorf("reason should mention 'test', got %q", g.Reason)
	}
}

func TestPlanCommits_TestSourceCoupling_TypeScript(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("src/api/users.ts",
			sc("getUser", "api.getUser", "src/api/users.ts", BodyChanged),
		),
		fsum("src/api/users.test.ts",
			sc("testGetUser", "api.testGetUser", "src/api/users.test.ts", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit (ts test+source), got %d", len(plan.Commits))
	}
	if plan.Commits[0].TestCount != 1 {
		t.Errorf("expected TestCount=1, got %d", plan.Commits[0].TestCount)
	}
}

func TestPlanCommits_TestSourceCoupling_Python(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("src/parser.py",
			sc("parse", "parser.parse", "src/parser.py", BodyChanged),
		),
		fsum("src/test_parser.py",
			sc("test_parse", "parser.test_parse", "src/test_parser.py", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit (py test+source), got %d", len(plan.Commits))
	}
	if plan.Commits[0].TestCount != 1 {
		t.Errorf("expected TestCount=1, got %d", plan.Commits[0].TestCount)
	}
}

func TestPlanCommits_TestSourceCoupling_Ruby(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("lib/parser.rb",
			sc("parse", "parser.parse", "lib/parser.rb", BodyChanged),
		),
		fsum("spec/parser_spec.rb",
			sc("test_parse", "parser_spec.test_parse", "spec/parser_spec.rb", Added),
		),
	}, nil)

	// Ruby spec file is under a different directory so source mapping won't find
	// the source — they stay separate. That's correct behaviour.
	// Just ensure we don't panic.
	if len(plan.Commits) == 0 {
		t.Error("expected at least one commit")
	}
}

// ---------------------------------------------------------------------------
// Breaking changes flagged
// ---------------------------------------------------------------------------

func TestPlanCommits_BreakingChanges(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/store/store.go",
			scBreaking("OpenStore", "store.OpenStore", "internal/store/store.go", SignatureChanged),
			sc("closeStore", "store.closeStore", "internal/store/store.go", BodyChanged),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	g := plan.Commits[0]
	if len(g.Breaking) != 1 {
		t.Errorf("expected 1 breaking symbol, got %d: %v", len(g.Breaking), g.Breaking)
	}
	if g.Breaking[0] != "OpenStore" {
		t.Errorf("expected breaking symbol OpenStore, got %q", g.Breaking[0])
	}
	if plan.Stats.Breaking != 1 {
		t.Errorf("expected Stats.Breaking=1, got %d", plan.Stats.Breaking)
	}
}

// ---------------------------------------------------------------------------
// Test count collapsed — no test summaries in Summaries
// ---------------------------------------------------------------------------

func TestPlanCommits_TestCountCollapsed(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/semdiff/differ.go",
			sc("Diff", "semdiff.Diff", "internal/semdiff/differ.go", BodyChanged),
		),
		fsum("internal/semdiff/differ_test.go",
			sc("TestDiff_A", "semdiff.TestDiff_A", "internal/semdiff/differ_test.go", Added),
			sc("TestDiff_B", "semdiff.TestDiff_B", "internal/semdiff/differ_test.go", Added),
			sc("TestDiff_C", "semdiff.TestDiff_C", "internal/semdiff/differ_test.go", Added),
			sc("TestDiff_D", "semdiff.TestDiff_D", "internal/semdiff/differ_test.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	g := plan.Commits[0]
	if g.TestCount != 4 {
		t.Errorf("expected TestCount=4, got %d", g.TestCount)
	}
	// None of the test function names should appear in Summaries.
	for _, s := range g.Summaries {
		if strings.Contains(s, "TestDiff") {
			t.Errorf("test summary leaked into Summaries: %q", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Scope derivation from paths
// ---------------------------------------------------------------------------

func TestPlanCommits_Scope_CommonDirectory(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pipeline/gitdiff.go",
			sc("ParseGitDiff", "pipeline.ParseGitDiff", "internal/pipeline/gitdiff.go", Added),
		),
		fsum("internal/pipeline/gitshow.go",
			sc("ShowGit", "pipeline.ShowGit", "internal/pipeline/gitshow.go", Added),
		),
	}, []CouplingEdge{
		{FromQN: "pipeline.ParseGitDiff", ToQN: "pipeline.ShowGit", Type: "CALLS"},
	})

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	if plan.Commits[0].Scope != "pipeline" {
		t.Errorf("expected scope=pipeline, got %q", plan.Commits[0].Scope)
	}
}

func TestPlanCommits_Scope_SingleFileNoDir(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("main.go",
			sc("main", "main.main", "main.go", BodyChanged),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	if plan.Commits[0].Scope != "main" {
		t.Errorf("expected scope=main for root file, got %q", plan.Commits[0].Scope)
	}
}

func TestPlanCommits_Scope_NestedPath(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("src/api/users.ts",
			sc("getUser", "api.getUser", "src/api/users.ts", BodyChanged),
		),
		fsum("src/api/auth.ts",
			sc("login", "api.login", "src/api/auth.ts", BodyChanged),
		),
	}, []CouplingEdge{
		{FromQN: "api.getUser", ToQN: "api.login", Type: "CALLS"},
	})

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	if plan.Commits[0].Scope != "api" {
		t.Errorf("expected scope=api, got %q", plan.Commits[0].Scope)
	}
}

func TestPlanCommits_Scope_SingleFile_Tools(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/tools/detect_changes.go",
			sc("DetectChanges", "tools.DetectChanges", "internal/tools/detect_changes.go", BodyChanged),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	if plan.Commits[0].Scope != "tools" {
		t.Errorf("expected scope=tools, got %q", plan.Commits[0].Scope)
	}
}

// ---------------------------------------------------------------------------
// Draft message generation
// ---------------------------------------------------------------------------

func TestPlanCommits_DraftMsg_Added(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pipeline/gitdiff.go",
			sc("ParseGitDiff", "pipeline.ParseGitDiff", "internal/pipeline/gitdiff.go", Added),
		),
	}, nil)

	msg := plan.Commits[0].DraftMsg
	if !strings.Contains(msg, "add") {
		t.Errorf("draft message for Added should contain 'add': %q", msg)
	}
	if !strings.Contains(msg, "pipeline") {
		t.Errorf("draft message should contain scope 'pipeline': %q", msg)
	}
}

func TestPlanCommits_DraftMsg_Removed(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/store/store.go",
			sc("OldFunc", "store.OldFunc", "internal/store/store.go", Removed),
		),
	}, nil)

	msg := plan.Commits[0].DraftMsg
	if !strings.Contains(msg, "remove") {
		t.Errorf("draft message for Removed should contain 'remove': %q", msg)
	}
}

func TestPlanCommits_DraftMsg_SignatureChanged(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/store/store.go",
			scSig("OpenStore", "store.OpenStore", "internal/store/store.go"),
		),
	}, nil)

	msg := plan.Commits[0].DraftMsg
	if !strings.Contains(msg, "update") {
		t.Errorf("draft message for SignatureChanged should contain 'update': %q", msg)
	}
	if !strings.Contains(msg, "OpenStore") {
		t.Errorf("draft message should contain symbol name: %q", msg)
	}
}

func TestPlanCommits_DraftMsg_BodyChanged(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/store/store.go",
			sc("compute", "store.compute", "internal/store/store.go", BodyChanged),
		),
	}, nil)

	msg := plan.Commits[0].DraftMsg
	if !strings.Contains(msg, "refactor") {
		t.Errorf("draft message for BodyChanged should contain 'refactor': %q", msg)
	}
}

func TestPlanCommits_DraftMsg_Renamed(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/svc/svc.go",
			scRenamed("handlePayment", "processPayment", "svc.handlePayment", "internal/svc/svc.go"),
		),
	}, nil)

	msg := plan.Commits[0].DraftMsg
	if !strings.Contains(msg, "rename") {
		t.Errorf("draft message for Renamed should contain 'rename': %q", msg)
	}
}

func TestPlanCommits_DraftMsg_Truncated(t *testing.T) {
	// Generate many symbols to trigger truncation.
	var changes []SymbolChange
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("VeryLongFunctionName%d", i)
		changes = append(changes, sc(name, "pkg."+name, "internal/pkg/pkg.go", Added))
	}

	plan := PlanCommits([]FileChangeSummary{
		{Path: "internal/pkg/pkg.go", Status: "M", Changes: changes},
	}, nil)

	msg := plan.Commits[0].DraftMsg
	if len([]rune(msg)) > 80 {
		t.Errorf("draft message should be ≤ 80 chars, got %d: %q", len([]rune(msg)), msg)
	}
}

func TestPlanCommits_DraftMsg_OnlyTests(t *testing.T) {
	// When the only non-test group is empty (test-only file with no source pair).
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pkg/pkg_test.go",
			sc("TestFoo", "pkg.TestFoo", "internal/pkg/pkg_test.go", Added),
			sc("TestBar", "pkg.TestBar", "internal/pkg/pkg_test.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	g := plan.Commits[0]
	if g.TestCount != 2 {
		t.Errorf("expected TestCount=2, got %d", g.TestCount)
	}
	// All changes are tests, so summaries should be empty.
	if len(g.Summaries) != 0 {
		t.Errorf("expected no non-test summaries, got %v", g.Summaries)
	}
	msg := g.DraftMsg
	if !strings.Contains(msg, "test") {
		t.Errorf("draft message for test-only commit should mention 'test': %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Details — only for structural changes
// ---------------------------------------------------------------------------

func TestPlanCommits_Details_OnlyStructural(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/svc/svc.go",
			scSig("Process", "svc.Process", "internal/svc/svc.go"),    // should have detail
			sc("helper", "svc.helper", "internal/svc/svc.go", BodyChanged), // no detail
			sc("newFunc", "svc.newFunc", "internal/svc/svc.go", Added),     // no detail
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	g := plan.Commits[0]
	if len(g.Details) != 1 {
		t.Errorf("expected 1 detail (SignatureChanged only), got %d: %+v", len(g.Details), g.Details)
	}
	if g.Details[0].Name != "Process" {
		t.Errorf("expected detail for Process, got %q", g.Details[0].Name)
	}
	if g.Details[0].Kind != SignatureChanged {
		t.Errorf("expected detail kind SignatureChanged, got %q", g.Details[0].Kind)
	}
}

func TestPlanCommits_Details_VisibilityChanged(t *testing.T) {
	sc2 := SymbolChange{
		Name:          "Handler",
		QualifiedName: "pkg.Handler",
		Label:         "Function",
		FilePath:      "pkg/handler.go",
		Kind:          VisibilityChanged,
		Deltas:        []FieldDelta{{Field: "is_exported", Old: "true", New: "false"}},
		Summary:       "visibility changed Handler",
	}
	plan := PlanCommits([]FileChangeSummary{
		{Path: "pkg/handler.go", Status: "M", Changes: []SymbolChange{sc2}},
	}, nil)

	g := plan.Commits[0]
	if len(g.Details) != 1 {
		t.Errorf("expected 1 detail for VisibilityChanged, got %d", len(g.Details))
	}
	if len(g.Details[0].Deltas) != 1 {
		t.Errorf("expected deltas preserved in detail, got %d", len(g.Details[0].Deltas))
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func TestPlanCommits_Stats(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/a/a.go",
			scBreaking("A", "a.A", "internal/a/a.go", SignatureChanged),
		),
		fsum("internal/b/b.go",
			sc("B", "b.B", "internal/b/b.go", Added),
		),
	}, nil)

	if plan.Stats.TotalFiles != 2 {
		t.Errorf("expected TotalFiles=2, got %d", plan.Stats.TotalFiles)
	}
	if plan.Stats.TotalChanges != 2 {
		t.Errorf("expected TotalChanges=2, got %d", plan.Stats.TotalChanges)
	}
	if plan.Stats.TotalCommits != 2 {
		t.Errorf("expected TotalCommits=2, got %d", plan.Stats.TotalCommits)
	}
	if plan.Stats.Breaking != 1 {
		t.Errorf("expected Breaking=1, got %d", plan.Stats.Breaking)
	}
}

// ---------------------------------------------------------------------------
// All changes in one file → single commit
// ---------------------------------------------------------------------------

func TestPlanCommits_AllInOneFile(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pipeline/parser.go",
			sc("Parse", "pipeline.Parse", "internal/pipeline/parser.go", Added),
			sc("parseToken", "pipeline.parseToken", "internal/pipeline/parser.go", Added),
			scSig("ParseLine", "pipeline.ParseLine", "internal/pipeline/parser.go"),
			sc("cleanup", "pipeline.cleanup", "internal/pipeline/parser.go", Removed),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit for single file, got %d", len(plan.Commits))
	}
	g := plan.Commits[0]
	if len(g.Files) != 1 {
		t.Errorf("expected 1 file, got %d: %v", len(g.Files), g.Files)
	}
	// All 4 changes are non-test.
	if len(g.Summaries) != 4 {
		t.Errorf("expected 4 summaries, got %d", len(g.Summaries))
	}
}

// ---------------------------------------------------------------------------
// Mixed: some coupled, some independent
// ---------------------------------------------------------------------------

func TestPlanCommits_MixedCoupledAndIndependent(t *testing.T) {
	// A and B are coupled; C is independent.
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pipeline/gitdiff.go",
			scSig("ParseGitDiff", "pipeline.ParseGitDiff", "internal/pipeline/gitdiff.go"),
		),
		fsum("internal/tools/detect_changes.go",
			sc("DetectChanges", "tools.DetectChanges", "internal/tools/detect_changes.go", BodyChanged),
		),
		fsum("internal/store/store.go",
			sc("OpenStore", "store.OpenStore", "internal/store/store.go", Added),
		),
	}, []CouplingEdge{
		{FromQN: "tools.DetectChanges", ToQN: "pipeline.ParseGitDiff", Type: "CALLS"},
	})

	if len(plan.Commits) != 2 {
		t.Fatalf("expected 2 commits (coupled pair + independent), got %d: %+v", len(plan.Commits), plan.Commits)
	}

	// The coupled pair should be in the same group.
	var coupledGroup *CommitGroup
	for i := range plan.Commits {
		files := plan.Commits[i].Files
		hasPipeline := false
		hasTools := false
		for _, f := range files {
			if strings.Contains(f, "pipeline") {
				hasPipeline = true
			}
			if strings.Contains(f, "tools") {
				hasTools = true
			}
		}
		if hasPipeline && hasTools {
			coupledGroup = &plan.Commits[i]
			break
		}
	}
	if coupledGroup == nil {
		t.Fatal("expected a group containing both pipeline and tools files")
	}
	if !strings.Contains(coupledGroup.Reason, "coupled") {
		t.Errorf("coupled group reason should mention 'coupled': %q", coupledGroup.Reason)
	}

	// The store group should be separate.
	_, hasStore := findGroup(plan.Commits, "internal/store/store.go")
	if !hasStore {
		t.Error("expected a separate commit group for store/store.go")
	}
}

// ---------------------------------------------------------------------------
// Reason field
// ---------------------------------------------------------------------------

func TestPlanCommits_Reason_SameFile(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/pkg/pkg.go",
			sc("A", "pkg.A", "internal/pkg/pkg.go", Added),
			sc("B", "pkg.B", "internal/pkg/pkg.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	if plan.Commits[0].Reason != "same file" {
		t.Errorf("expected reason='same file', got %q", plan.Commits[0].Reason)
	}
}

func TestPlanCommits_Reason_CoupledEdge(t *testing.T) {
	plan := PlanCommits(
		[]FileChangeSummary{
			fsum("a/a.go", sc("Foo", "a.Foo", "a/a.go", SignatureChanged)),
			fsum("b/b.go", sc("Bar", "b.Bar", "b/b.go", BodyChanged)),
		},
		[]CouplingEdge{{FromQN: "b.Bar", ToQN: "a.Foo", Type: "CALLS"}},
	)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	reason := plan.Commits[0].Reason
	if !strings.Contains(reason, "coupled") {
		t.Errorf("expected reason to contain 'coupled', got %q", reason)
	}
	if !strings.Contains(reason, "calls") {
		t.Errorf("expected reason to contain 'calls', got %q", reason)
	}
}

func TestPlanCommits_Reason_TestSource(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/svc/svc.go",
			sc("Process", "svc.Process", "internal/svc/svc.go", BodyChanged),
		),
		fsum("internal/svc/svc_test.go",
			sc("TestProcess", "svc.TestProcess", "internal/svc/svc_test.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
	if plan.Commits[0].Reason != "test + source" {
		t.Errorf("expected reason='test + source', got %q", plan.Commits[0].Reason)
	}
}

// ---------------------------------------------------------------------------
// Sorting: shorter/more core paths first
// ---------------------------------------------------------------------------

func TestPlanCommits_SortOrder(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/tools/deep/nested/file.go",
			sc("A", "deep.A", "internal/tools/deep/nested/file.go", Added),
		),
		fsum("main.go",
			sc("B", "main.B", "main.go", BodyChanged),
		),
		fsum("internal/svc/svc.go",
			sc("C", "svc.C", "internal/svc/svc.go", Added),
		),
	}, nil)

	if len(plan.Commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(plan.Commits))
	}
	// main.go (depth 0) should come before the others.
	if plan.Commits[0].Files[0] != "main.go" {
		t.Errorf("expected main.go first (shallowest path), got %q", plan.Commits[0].Files[0])
	}
	// The deep nested file should come last.
	lastFiles := plan.Commits[len(plan.Commits)-1].Files
	if !strings.Contains(lastFiles[0], "deep") {
		t.Errorf("expected deepest path last, got %q", lastFiles[0])
	}
}

// ---------------------------------------------------------------------------
// isTestFile unit tests
// ---------------------------------------------------------------------------

func TestIsTestFile(t *testing.T) {
	cases := []struct {
		path     string
		expected bool
	}{
		{"internal/semdiff/differ_test.go", true},
		{"internal/semdiff/differ.go", false},
		{"src/api/users.test.ts", true},
		{"src/api/users.ts", false},
		{"src/api/users.test.js", true},
		{"src/api/users.test.tsx", true},
		{"src/api/users.test.jsx", true},
		{"src/api/users.spec.ts", true},
		{"src/api/users.spec.js", true},
		{"src/parser_test.py", true},
		{"src/test_parser.py", true},
		{"src/parser.py", false},
		{"spec/parser_spec.rb", true},
		{"lib/parser.rb", false},
		{"main.go", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := isTestFile(tc.path)
			if got != tc.expected {
				t.Errorf("isTestFile(%q) = %v, want %v", tc.path, got, tc.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// correspondingSourceFile unit tests
// ---------------------------------------------------------------------------

func TestCorrespondingSourceFile(t *testing.T) {
	cases := []struct {
		testPath string
		srcPath  string
	}{
		{"internal/semdiff/differ_test.go", "internal/semdiff/differ.go"},
		{"src/parser_test.py", "src/parser.py"},
		{"src/test_parser.py", "src/parser.py"},
		{"src/api/users.test.ts", "src/api/users.ts"},
		{"src/api/users.test.js", "src/api/users.js"},
		{"src/api/users.test.tsx", "src/api/users.tsx"},
		{"src/api/users.test.jsx", "src/api/users.jsx"},
		{"src/api/users.spec.ts", "src/api/users.ts"},
		{"src/api/users.spec.js", "src/api/users.js"},
		{"lib/parser_spec.rb", "lib/parser.rb"},
	}

	for _, tc := range cases {
		t.Run(tc.testPath, func(t *testing.T) {
			got := correspondingSourceFile(tc.testPath)
			if got != tc.srcPath {
				t.Errorf("correspondingSourceFile(%q) = %q, want %q", tc.testPath, got, tc.srcPath)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// deriveScope unit tests
// ---------------------------------------------------------------------------

func TestDeriveScope(t *testing.T) {
	cases := []struct {
		files []string
		scope string
	}{
		{[]string{"internal/pipeline/gitdiff.go", "internal/pipeline/gitshow.go"}, "pipeline"},
		{[]string{"internal/tools/detect_changes.go"}, "tools"},
		{[]string{"src/api/users.ts", "src/api/auth.ts"}, "api"},
		{[]string{"main.go"}, "main"},
		{[]string{"config.yaml"}, "config"},
		{[]string{"a/x.go", "b/y.go"}, "root"},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.files, ","), func(t *testing.T) {
			got := deriveScope(tc.files)
			if got != tc.scope {
				t.Errorf("deriveScope(%v) = %q, want %q", tc.files, got, tc.scope)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Edge cases for coupling
// ---------------------------------------------------------------------------

func TestPlanCommits_CouplingEdge_OnlyOneEndInChangeset(t *testing.T) {
	// Edge references a QN not in the changeset → should not cause a panic or union.
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/a/a.go",
			sc("A", "a.A", "internal/a/a.go", Added),
		),
	}, []CouplingEdge{
		{FromQN: "a.A", ToQN: "notInChangeset.B", Type: "CALLS"},
	})

	// Should still produce 1 commit for the single changed file.
	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
}

func TestPlanCommits_CouplingEdge_BothEndsNotInChangeset(t *testing.T) {
	plan := PlanCommits([]FileChangeSummary{
		fsum("internal/a/a.go",
			sc("A", "a.A", "internal/a/a.go", Added),
		),
	}, []CouplingEdge{
		{FromQN: "x.X", ToQN: "y.Y", Type: "CALLS"},
	})

	if len(plan.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(plan.Commits))
	}
}

