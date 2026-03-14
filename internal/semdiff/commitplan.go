package semdiff

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// indexedChange associates a SymbolChange with the file it came from.
// Package-level so it can be used by buildCommitGroup and deriveReason helpers.
type indexedChange struct {
	change   SymbolChange
	filePath string
}

// PlanCommits groups symbol changes into logical commit boundaries using
// coupling edges (graph relationships between changed symbols) and file
// co-location. Returns a CommitPlan with suggested commits.
func PlanCommits(fileSummaries []FileChangeSummary, couplings []CouplingEdge) CommitPlan {
	if len(fileSummaries) == 0 {
		return CommitPlan{Stats: PlanStats{}}
	}

	// Build a flat list of all changes with their file path, and an index
	// from QualifiedName to SymbolChange for quick lookup.
	var allChanges []indexedChange
	qnIndex := make(map[string]int) // QN → index in allChanges

	for _, fs := range fileSummaries {
		for _, sc := range fs.Changes {
			idx := len(allChanges)
			qnIndex[sc.QualifiedName] = idx
			allChanges = append(allChanges, indexedChange{change: sc, filePath: fs.Path})
		}
	}

	if len(allChanges) == 0 {
		return CommitPlan{Stats: PlanStats{TotalFiles: len(fileSummaries)}}
	}

	// -----------------------------------------------------------------------
	// Step 1: Union-Find grouping
	// -----------------------------------------------------------------------
	uf := newUnionFind()
	for _, ic := range allChanges {
		uf.add(ic.change.QualifiedName)
	}

	// 1a. Coupling edges: union symbols that are explicitly coupled.
	for _, edge := range couplings {
		_, fromOK := qnIndex[edge.FromQN]
		_, toOK := qnIndex[edge.ToQN]
		if fromOK && toOK {
			uf.union(edge.FromQN, edge.ToQN)
		}
	}

	// 1b. File co-location: all changes in the same file belong together.
	for _, fs := range fileSummaries {
		if len(fs.Changes) < 2 {
			continue
		}
		first := fs.Changes[0].QualifiedName
		for i := 1; i < len(fs.Changes); i++ {
			uf.union(first, fs.Changes[i].QualifiedName)
		}
	}

	// 1c. Test-source coupling: union test file changes with their source file.
	// Build a map from file path → QNs in that file.
	fileToQNs := make(map[string][]string)
	for _, fs := range fileSummaries {
		for _, sc := range fs.Changes {
			fileToQNs[fs.Path] = append(fileToQNs[fs.Path], sc.QualifiedName)
		}
	}

	for _, fs := range fileSummaries {
		if !isTestFile(fs.Path) {
			continue
		}
		srcPath := correspondingSourceFile(fs.Path)
		if srcPath == "" {
			continue
		}
		srcQNs, ok := fileToQNs[srcPath]
		if !ok || len(srcQNs) == 0 {
			continue
		}
		// Union all test QNs with the first source QN (they'll all get connected
		// transitively via the file co-location union from step 1b).
		testQNs := fileToQNs[fs.Path]
		if len(testQNs) == 0 {
			continue
		}
		uf.union(testQNs[0], srcQNs[0])
	}

	// -----------------------------------------------------------------------
	// Step 2: Extract connected components.
	// -----------------------------------------------------------------------
	components := make(map[string][]indexedChange) // root → changes
	for _, ic := range allChanges {
		root := uf.find(ic.change.QualifiedName)
		components[root] = append(components[root], ic)
	}

	// -----------------------------------------------------------------------
	// Step 3: Build CommitGroup for each component.
	// -----------------------------------------------------------------------
	var groups []CommitGroup
	for root, ics := range components {
		groups = append(groups, buildCommitGroup(root, ics, couplings, qnIndex))
	}

	// -----------------------------------------------------------------------
	// Step 4: Sort groups (shorter/core paths first, then alphabetically).
	// -----------------------------------------------------------------------
	sort.Slice(groups, func(i, j int) bool {
		// Fewer path components = more "core".
		iDepth := strings.Count(groups[i].Files[0], "/")
		jDepth := strings.Count(groups[j].Files[0], "/")
		if iDepth != jDepth {
			return iDepth < jDepth
		}
		return groups[i].Files[0] < groups[j].Files[0]
	})

	// -----------------------------------------------------------------------
	// Step 5: Build stats.
	// -----------------------------------------------------------------------
	totalBreaking := 0
	for _, g := range groups {
		totalBreaking += len(g.Breaking)
	}

	uniqueFiles := make(map[string]bool)
	for _, fs := range fileSummaries {
		uniqueFiles[fs.Path] = true
	}

	totalChanges := 0
	for _, fs := range fileSummaries {
		totalChanges += len(fs.Changes)
	}

	return CommitPlan{
		Commits: groups,
		Stats: PlanStats{
			TotalChanges: totalChanges,
			TotalFiles:   len(uniqueFiles),
			TotalCommits: len(groups),
			Breaking:     totalBreaking,
		},
	}
}

// ---------------------------------------------------------------------------
// CommitGroup construction
// ---------------------------------------------------------------------------

func buildCommitGroup(root string, ics []indexedChange, couplings []CouplingEdge, qnIndex map[string]int) CommitGroup {
	// Collect unique files and separate test vs. non-test changes.
	fileSet := make(map[string]bool)
	var nonTestChanges []SymbolChange
	testCount := 0

	for _, ic := range ics {
		fileSet[ic.filePath] = true
		if isTestFile(ic.filePath) {
			testCount++
		} else {
			nonTestChanges = append(nonTestChanges, ic.change)
		}
	}

	files := sortedKeys(fileSet)

	// Derive scope from the common path prefix.
	scope := deriveScope(files)

	// Build compact summaries from non-test changes only.
	// Format: "M Function ParseGitDiffFiles — signature changed (old → new)"
	// Uses short markers: A=added, D=removed, M=modified, R=renamed, V=visibility
	summaries := make([]string, 0, len(nonTestChanges))
	for _, sc := range nonTestChanges {
		summaries = append(summaries, compactSummary(sc))
	}

	// Build details for structural changes only.
	var details []ChangeDetail
	var breaking []string
	for _, ic := range ics {
		sc := ic.change
		switch sc.Kind {
		case SignatureChanged, VisibilityChanged, Renamed:
			details = append(details, ChangeDetail{
				Name:   sc.Name,
				Kind:   sc.Kind,
				Deltas: sc.Deltas,
			})
		}
		if sc.IsBreaking {
			breaking = append(breaking, sc.Name)
		}
	}

	// Determine reason for grouping.
	reason := deriveReason(ics, couplings, qnIndex)

	// Generate draft message.
	draftMsg := buildDraftMsg(scope, nonTestChanges, testCount)

	return CommitGroup{
		Files:     files,
		Summaries: summaries,
		Details:   details,
		Breaking:  breaking,
		TestCount: testCount,
		Scope:     scope,
		DraftMsg:  draftMsg,
		Reason:    reason,
	}
}

// deriveScope returns a short scope string from the common file path prefix.
func deriveScope(files []string) string {
	if len(files) == 0 {
		return "root"
	}

	// Find the common directory prefix of all files.
	commonDir := path.Dir(files[0])
	for _, f := range files[1:] {
		d := path.Dir(f)
		commonDir = commonPrefix(commonDir, d)
	}

	// Use the last meaningful directory component.
	if commonDir != "" && commonDir != "." {
		return path.Base(commonDir)
	}

	// Single file or root-level files: use the filename without extension.
	if len(files) == 1 {
		base := path.Base(files[0])
		ext := path.Ext(base)
		if ext != "" {
			return strings.TrimSuffix(base, ext)
		}
		return base
	}

	return "root"
}

// commonPrefix returns the longest common path prefix of two slash-separated paths.
func commonPrefix(a, b string) string {
	aParts := strings.Split(a, "/")
	bParts := strings.Split(b, "/")
	var common []string
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] != bParts[i] {
			break
		}
		common = append(common, aParts[i])
	}
	if len(common) == 0 {
		return ""
	}
	return strings.Join(common, "/")
}

// deriveReason produces a human-readable explanation for why this group is together.
func deriveReason(ics []indexedChange, couplings []CouplingEdge, qnIndex map[string]int) string {
	// Build the set of QNs in this component.
	qns := make(map[string]bool)
	for _, ic := range ics {
		qns[ic.change.QualifiedName] = true
	}

	// Check for coupling edges within this group.
	for _, edge := range couplings {
		if qns[edge.FromQN] && qns[edge.ToQN] {
			// Use the names (not QNs) if they differ.
			fromName := lastName(edge.FromQN)
			toName := lastName(edge.ToQN)
			rel := strings.ToLower(edge.Type)
			return fmt.Sprintf("coupled: %s %s %s", fromName, rel, toName)
		}
	}

	// Check for test-source coupling.
	files := make(map[string]bool)
	for _, ic := range ics {
		files[ic.filePath] = true
	}
	hasTest := false
	hasSource := false
	for f := range files {
		if isTestFile(f) {
			hasTest = true
		} else {
			hasSource = true
		}
	}
	if hasTest && hasSource {
		return "test + source"
	}

	// Default: file co-location.
	return "same file"
}

// lastName returns the last segment of a dot- or slash-separated qualified name.
func lastName(qn string) string {
	if i := strings.LastIndexAny(qn, "./"); i >= 0 {
		return qn[i+1:]
	}
	return qn
}

// buildDraftMsg generates a neutral commit message from the non-test changes.
func buildDraftMsg(scope string, changes []SymbolChange, testCount int) string {
	if len(changes) == 0 && testCount > 0 {
		return fmt.Sprintf("%s: add %d test functions", scope, testCount)
	}
	if len(changes) == 0 {
		return fmt.Sprintf("%s: update", scope)
	}

	// Count dominant change kinds.
	kindCounts := make(map[ChangeKind]int)
	for _, sc := range changes {
		kindCounts[sc.Kind]++
	}

	// Collect names per kind for short messages.
	namesByKind := make(map[ChangeKind][]string)
	for _, sc := range changes {
		namesByKind[sc.Kind] = append(namesByKind[sc.Kind], sc.Name)
	}

	// Build verb + description fragments, in priority order.
	type fragment struct {
		verb string
		desc string
	}
	var frags []fragment

	const maxNames = 3 // show at most this many names before summarising

	addFrag := func(kind ChangeKind, verb string) {
		if count := kindCounts[kind]; count > 0 {
			names := namesByKind[kind]
			frags = append(frags, fragment{
				verb: verb,
				desc: compactNames(names, maxNames, scope),
			})
		}
	}

	// Priority: Renamed > SignatureChanged > VisibilityChanged > Added > Removed > BodyChanged
	if kindCounts[Renamed] > 0 {
		for _, sc := range changes {
			if sc.Kind == Renamed {
				frags = append(frags, fragment{
					verb: "rename",
					desc: fmt.Sprintf("%s to %s", sc.OldName, sc.Name),
				})
			}
		}
	}
	addFrag(SignatureChanged, "update")
	addFrag(VisibilityChanged, "update")
	addFrag(Added, "add")
	addFrag(Removed, "remove")
	addFrag(BodyChanged, "refactor")

	if len(frags) == 0 {
		return fmt.Sprintf("%s: update", scope)
	}

	// Collapse if there's only one fragment type.
	if len(frags) == 1 {
		msg := fmt.Sprintf("%s: %s %s", scope, frags[0].verb, frags[0].desc)
		return truncate(msg, 80)
	}

	// Multiple fragment types: combine up to 2, then summarise.
	parts := make([]string, 0, len(frags))
	for _, f := range frags {
		parts = append(parts, fmt.Sprintf("%s %s", f.verb, f.desc))
	}
	combined := fmt.Sprintf("%s: %s", scope, strings.Join(parts, ", "))
	return truncate(combined, 80)
}

// compactNames formats a list of symbol names for use in a commit message.
// If the list is short enough, names are joined. If too many, it summarises.
func compactNames(names []string, max int, scope string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%d functions in %s", len(names), scope)
}

// changeKindMarker returns a short git-style marker for a ChangeKind.
func changeKindMarker(kind ChangeKind) string {
	switch kind {
	case Added:
		return "A"
	case Removed:
		return "D"
	case Renamed:
		return "R"
	case VisibilityChanged:
		return "V"
	default: // SignatureChanged, BodyChanged
		return "M"
	}
}

// compactSummary produces a short one-liner for a commit plan summary.
// Format: "M Function foo — signature changed (old → new)"
// For simple cases: "A Function bar" or "D Class Baz"
func compactSummary(sc SymbolChange) string {
	marker := changeKindMarker(sc.Kind)
	prefix := fmt.Sprintf("%s %s %s", marker, sc.Label, sc.Name)

	switch sc.Kind {
	case Added, Removed:
		return prefix
	case Renamed:
		return fmt.Sprintf("%s — from %s", prefix, sc.OldName)
	default:
		// For modified symbols, append a brief description of what changed.
		var parts []string
		for _, d := range sc.Deltas {
			switch d.Field {
			case "signature":
				parts = append(parts, fmt.Sprintf("sig (%s → %s)", d.Old, d.New))
			case "param_types":
				parts = append(parts, fmt.Sprintf("params (%s → %s)", d.Old, d.New))
			case "return_type":
				parts = append(parts, fmt.Sprintf("returns (%s → %s)", d.Old, d.New))
			case "is_exported":
				if d.New == "true" {
					parts = append(parts, "exported")
				} else {
					parts = append(parts, "unexported")
				}
			case "decorators", "base_classes":
				parts = append(parts, d.Field+" changed")
			case "docstring":
				parts = append(parts, "docs")
			case "complexity":
				parts = append(parts, fmt.Sprintf("complexity %s→%s", d.Old, d.New))
			case "lines":
				parts = append(parts, fmt.Sprintf("lines %s→%s", d.Old, d.New))
			}
		}
		if len(parts) == 0 {
			return prefix
		}
		return fmt.Sprintf("%s — %s", prefix, strings.Join(parts, ", "))
	}
}

// truncate trims a string to at most maxLen runes, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

// ---------------------------------------------------------------------------
// Test file detection and source mapping
// ---------------------------------------------------------------------------

// isTestFile returns true if the file path matches known test file patterns.
func isTestFile(filePath string) bool {
	base := path.Base(filePath)
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, "_test.py") ||
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
		strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, ".test.tsx") ||
		strings.HasSuffix(base, ".test.jsx") ||
		strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".spec.js") ||
		strings.HasSuffix(base, "_spec.rb")
}

// correspondingSourceFile returns the expected source file path for a test file,
// or an empty string if the pattern is not recognised.
func correspondingSourceFile(testPath string) string {
	dir := path.Dir(testPath)
	base := path.Base(testPath)

	// Go: foo_test.go → foo.go
	if strings.HasSuffix(base, "_test.go") {
		src := strings.TrimSuffix(base, "_test.go") + ".go"
		return path.Join(dir, src)
	}

	// Python: foo_test.py → foo.py
	if strings.HasSuffix(base, "_test.py") {
		src := strings.TrimSuffix(base, "_test.py") + ".py"
		return path.Join(dir, src)
	}

	// Python: test_foo.py → foo.py
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		src := strings.TrimPrefix(base, "test_")
		return path.Join(dir, src)
	}

	// TypeScript/JavaScript: foo.test.ts → foo.ts, foo.test.js → foo.js, etc.
	for _, testSuffix := range []string{".test.tsx", ".test.ts", ".test.jsx", ".test.js", ".spec.ts", ".spec.js"} {
		if strings.HasSuffix(base, testSuffix) {
			ext := path.Ext(testSuffix) // e.g. ".ts"
			inner := strings.TrimSuffix(testSuffix, ext)
			inner = strings.TrimPrefix(inner, ".")
			src := strings.TrimSuffix(base, testSuffix) + ext
			_ = inner
			return path.Join(dir, src)
		}
	}

	// Ruby: foo_spec.rb → foo.rb
	if strings.HasSuffix(base, "_spec.rb") {
		src := strings.TrimSuffix(base, "_spec.rb") + ".rb"
		return path.Join(dir, src)
	}

	return ""
}

// ---------------------------------------------------------------------------
// Union-Find
// ---------------------------------------------------------------------------

type unionFind struct {
	parent map[string]string
}

func newUnionFind() *unionFind {
	return &unionFind{parent: make(map[string]string)}
}

func (u *unionFind) add(key string) {
	if _, ok := u.parent[key]; !ok {
		u.parent[key] = key
	}
}

func (u *unionFind) find(key string) string {
	if u.parent[key] == key {
		return key
	}
	// Path compression.
	u.parent[key] = u.find(u.parent[key])
	return u.parent[key]
}

func (u *unionFind) union(a, b string) {
	ra := u.find(a)
	rb := u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
