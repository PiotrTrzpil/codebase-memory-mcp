package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/semdiff"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerSemanticDiff() {
	s.addTool(&mcp.Tool{
		Name:        "semantic_diff",
		Description: "Compare old and new versions of changed files at the AST level, producing structured descriptions of what changed about each symbol (e.g. 'parameter added', 'return type changed', 'export removed'). Builds on detect_changes infrastructure but adds dual-version extraction and symbol-level comparison. Detects added/removed/renamed symbols, signature changes, body-only changes, and flags breaking changes for exported symbols. Requires git in PATH.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"scope": {
					"type": "string",
					"description": "Which changes to analyze: 'unstaged' (working tree), 'staged' (git add), 'all' (HEAD, default), 'branch' (compare with base_branch), 'commits' (compare from_ref...to_ref)",
					"enum": ["unstaged", "staged", "all", "branch", "commits"]
				},
				"base_branch": {
					"type": "string",
					"description": "Base branch for scope=branch comparison (default: main). For scope=commits, acts as from_ref (default: HEAD~1)."
				},
				"to_ref": {
					"type": "string",
					"description": "Target ref for scope=commits (default: HEAD). Ignored for other scopes."
				},
				"depth": {
					"type": "integer",
					"description": "Maximum BFS depth for impact tracing (1-5, default 3)"
				},
				"max_impact": {
					"type": "integer",
					"description": "Maximum number of impacted symbols to return (default: 50)"
				},
				"include_impact": {
					"type": "boolean",
					"description": "When true (default), trace impact of changed symbols via BFS. Set false to skip expensive tracing."
				},
				"breaking_only": {
					"type": "boolean",
					"description": "When true, only include symbols where IsBreaking=true in the files output. Summary counts reflect the full change set."
				},
				"labels": {
					"type": "string",
					"description": "Comma-separated list of labels to include (e.g. 'Function,Method'). Case-insensitive. Filters the files output; summary counts reflect the full set."
				},
				"file_pattern": {
					"type": "string",
					"description": "Glob pattern to filter which files are analyzed (e.g. 'internal/**/*.go' or '*.ts'). Applied before fetching file contents."
				},
				"summary_only": {
					"type": "boolean",
					"description": "When true, return only the summary counts (and warnings if any), omitting the files, breaking_changes, and impact lists."
				},
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			}
		}`),
	}, s.handleSemanticDiff)
}

func (s *Server) handleSemanticDiff(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	dp := parseDiffParams(args)

	includeImpact := true
	if v, ok := args["include_impact"]; ok {
		if b, ok := v.(bool); ok {
			includeImpact = b
		}
	}

	breakingOnly := getBoolArg(args, "breaking_only")

	// Parse labels filter: split on comma, trim, lowercase for case-insensitive matching
	var labelFilter []string
	if labelsStr := getStringArg(args, "labels"); labelsStr != "" {
		for _, l := range strings.Split(labelsStr, ",") {
			l = strings.ToLower(strings.TrimSpace(l))
			if l != "" {
				labelFilter = append(labelFilter, l)
			}
		}
	}

	filePattern := getStringArg(args, "file_pattern")

	project := getStringArg(args, "project")
	effectiveProject := s.resolveProjectName(project)

	st, repoPath, projName, resolveErr := s.resolveDetectRepo(effectiveProject)
	if resolveErr != nil {
		return resolveErr, nil
	}

	// Parse changed files
	changedFiles, err := pipeline.ParseGitDiffFiles(repoPath, dp.Scope, dp.BaseBranch, dp.ToRef)
	if err != nil {
		return errResult(fmt.Sprintf("git diff: %v", err)), nil
	}

	// Apply file_pattern filter early, before fetching file contents
	if filePattern != "" {
		filtered := changedFiles[:0]
		for _, f := range changedFiles {
			if matchFilePattern(filePattern, f.Path) {
				filtered = append(filtered, f)
			}
		}
		changedFiles = filtered
	}

	if len(changedFiles) == 0 {
		responseData := buildEmptySemanticDiffResponse()
		s.addIndexStatus(responseData)
		result := s.result(responseData)
		s.addUpdateNotice(result)
		return result, nil
	}

	// Parse hunks for line-level mapping (used for impact tracing)
	hunks, err := pipeline.ParseGitDiffHunks(repoPath, dp.Scope, dp.BaseBranch, dp.ToRef)
	if err != nil {
		slog.Warn("semantic_diff.hunks.err", "err", err)
	}

	// Fetch old file contents
	oldContents, err := pipeline.OldFileContents(repoPath, dp.Scope, dp.BaseBranch, changedFiles)
	if err != nil {
		return errResult(fmt.Sprintf("fetch old contents: %v", err)), nil
	}

	// Per-file: extract old and new definitions, compute symbol changes
	var warnings []string
	var allFileSummaries []semdiff.FileChangeSummary
	var allChanges []semdiff.SymbolChange

	// Build def maps for breaking change classification
	oldDefMap := make(map[string]cbm.Definition)
	newDefMap := make(map[string]cbm.Definition)

	for _, f := range changedFiles {
		// Determine old definitions
		var oldDefs []cbm.Definition
		oldKey := f.Path
		if f.OldPath != "" {
			oldKey = f.OldPath
		}
		if oldContent, ok := oldContents[oldKey]; ok && len(oldContent) > 0 {
			relPath := oldKey
			l, ok := languageForFile(relPath)
			if !ok {
				slog.Debug("semantic_diff.skip_old_lang", "path", relPath)
			} else {
				fr, err := cbm.ExtractFile(oldContent, l, projName, relPath)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("parse old %s: %v", relPath, err))
					slog.Warn("semantic_diff.parse_old_err", "path", relPath, "err", err)
				} else if fr != nil {
					oldDefs = fr.Definitions
					for _, d := range oldDefs {
						oldDefMap[d.QualifiedName] = d
					}
				}
			}
		}

		// Determine new definitions (only for non-deleted files)
		var newDefs []cbm.Definition
		if f.Status != "D" {
			newContent, newErr := readNewFileContent(repoPath, f.Path, dp.Scope)
			if newErr != nil {
				warnings = append(warnings, fmt.Sprintf("read new %s: %v", f.Path, newErr))
				slog.Warn("semantic_diff.read_new_err", "path", f.Path, "err", newErr)
			} else if len(newContent) > 0 {
				l, ok := languageForFile(f.Path)
				if !ok {
					slog.Debug("semantic_diff.skip_new_lang", "path", f.Path)
				} else {
					fr, err := cbm.ExtractFile(newContent, l, projName, f.Path)
					if err != nil {
						warnings = append(warnings, fmt.Sprintf("parse new %s: %v", f.Path, err))
						slog.Warn("semantic_diff.parse_new_err", "path", f.Path, "err", err)
					} else if fr != nil {
						newDefs = fr.Definitions
						for _, d := range newDefs {
							newDefMap[d.QualifiedName] = d
						}
					}
				}
			}
		}

		changes := semdiff.Diff(oldDefs, newDefs, f.Path, f.Status)

		summary := semdiff.FileChangeSummary{
			Path:    f.Path,
			Status:  f.Status,
			OldPath: f.OldPath,
			Changes: changes,
		}
		allFileSummaries = append(allFileSummaries, summary)
		allChanges = append(allChanges, changes...)
	}

	// Classify breaking changes across all files
	breakingChanges := semdiff.ClassifyBreaking(allChanges, oldDefMap, newDefMap)

	// Build diff summary counts from the full (unfiltered) set
	diffSummary := buildDiffSummary(allFileSummaries, changedFiles, breakingChanges)

	// Impact tracing (optional, reuses detect_changes helpers)
	var impactEntries []semdiff.ImpactEntry
	var allEdges []store.EdgeInfo
	if includeImpact {
		changedSymbols := mapChangesToSymbols(st, projName, changedFiles, hunks)
		impactedSymbols, edges := traceImpact(st, changedSymbols, dp.Depth)
		allEdges = edges

		// Cap to MaxImpact
		if len(impactedSymbols) > dp.MaxImpact {
			impactedSymbols = impactedSymbols[:dp.MaxImpact]
		}

		for _, is := range impactedSymbols {
			impactEntries = append(impactEntries, semdiff.ImpactEntry{
				Name:      is.Node.Name,
				Label:     is.Node.Label,
				File:      is.Node.FilePath,
				Risk:      string(store.HopToRisk(is.Hop)),
				Hop:       is.Hop,
				ChangedBy: is.ChangedBy,
			})
		}
	}

	// Update cross-service flag from edges
	for _, e := range allEdges {
		if e.Type == "HTTP_CALLS" || e.Type == "ASYNC_CALLS" {
			diffSummary.HasCrossService = true
			break
		}
	}
	diffSummary.ImpactedCount = len(impactEntries)

	// summary_only: return only counts, omit large lists
	if dp.SummaryOnly {
		responseData := map[string]any{"summary": diffSummary}
		if len(warnings) > 0 {
			responseData["warnings"] = warnings
		}
		s.addIndexStatus(responseData)
		result := s.result(responseData)
		s.addUpdateNotice(result)
		return result, nil
	}

	// Apply breaking_only and labels filters to the files output (after classification)
	displaySummaries := allFileSummaries
	if breakingOnly || len(labelFilter) > 0 {
		displaySummaries = filterFileSummaries(allFileSummaries, breakingOnly, labelFilter)
	}

	responseData := map[string]any{
		"files":            displaySummaries,
		"breaking_changes": breakingChanges,
		"impact":           impactEntries,
		"summary":          diffSummary,
	}
	if len(warnings) > 0 {
		responseData["warnings"] = warnings
	}

	s.addIndexStatus(responseData)
	result := s.result(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

// readNewFileContent reads the current (new) content of a file.
// For staged scope, reads from the git index (git show :0:<path>).
// For other scopes, reads from the filesystem.
func readNewFileContent(repoPath, filePath string, scope pipeline.DiffScope) ([]byte, error) {
	if scope == pipeline.DiffStaged {
		return gitShowIndex(repoPath, filePath)
	}
	return os.ReadFile(filepath.Join(repoPath, filePath))
}

// gitShowIndex reads a file from the git staging area using `git show :0:<path>`.
func gitShowIndex(repoPath, filePath string) ([]byte, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git not found in PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	arg := ":0:" + filePath
	cmd := exec.CommandContext(ctx, gitBin, "show", arg)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s: %w", arg, err)
	}
	return out, nil
}

// languageForFile returns the Language for a file path based on extension or filename.
func languageForFile(filePath string) (lang.Language, bool) {
	base := filepath.Base(filePath)
	// Try exact filename first (Makefile, Dockerfile, etc.)
	if l, ok := lang.LanguageForFilename(base); ok {
		return l, true
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == "" {
		return "", false
	}
	return lang.LanguageForExtension(ext)
}

// buildDiffSummary computes aggregate counts for the SemanticDiffResult.
func buildDiffSummary(files []semdiff.FileChangeSummary, changedFiles []pipeline.ChangedFile, breaking []semdiff.SymbolChange) semdiff.DiffSummary {
	var added, removed, modified int
	for _, f := range files {
		for _, c := range f.Changes {
			switch c.Kind {
			case semdiff.Added:
				added++
			case semdiff.Removed:
				removed++
			default:
				modified++
			}
		}
	}
	return semdiff.DiffSummary{
		FilesChanged:    len(changedFiles),
		SymbolsAdded:    added,
		SymbolsRemoved:  removed,
		SymbolsModified: modified,
		BreakingCount:   len(breaking),
	}
}

// matchFilePattern returns true if filePath matches the given glob pattern.
// Supports ** as a multi-segment wildcard by splitting on ** and checking prefix/suffix.
// Falls back to filepath.Match for patterns without **.
func matchFilePattern(pattern, filePath string) bool {
	if !strings.Contains(pattern, "**") {
		matched, err := filepath.Match(pattern, filePath)
		if err != nil {
			return false
		}
		return matched
	}
	// Split on first ** — check prefix before ** and suffix after **
	parts := strings.SplitN(pattern, "**", 2)
	prefix := parts[0]
	suffix := parts[1]
	if prefix != "" && !strings.HasPrefix(filePath, prefix) {
		return false
	}
	if suffix == "" {
		return true
	}
	// suffix may start with "/" — match remainder of path against suffix pattern
	remainder := filePath
	if prefix != "" {
		remainder = strings.TrimPrefix(filePath, prefix)
	}
	// Use filepath.Match on the suffix portion
	matched, err := filepath.Match(strings.TrimPrefix(suffix, "/"), strings.TrimPrefix(remainder, "/"))
	if err != nil {
		return false
	}
	return matched
}

// filterFileSummaries returns a copy of file summaries with symbol changes filtered
// according to breakingOnly and labelFilter. Files with no remaining changes are omitted.
func filterFileSummaries(summaries []semdiff.FileChangeSummary, breakingOnly bool, labelFilter []string) []semdiff.FileChangeSummary {
	var result []semdiff.FileChangeSummary
	for _, fs := range summaries {
		var filtered []semdiff.SymbolChange
		for _, c := range fs.Changes {
			if breakingOnly && !c.IsBreaking {
				continue
			}
			if len(labelFilter) > 0 {
				labelLower := strings.ToLower(c.Label)
				found := false
				for _, allowed := range labelFilter {
					if labelLower == allowed {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			filtered = append(filtered, c)
		}
		if len(filtered) > 0 {
			result = append(result, semdiff.FileChangeSummary{
				Path:    fs.Path,
				Status:  fs.Status,
				OldPath: fs.OldPath,
				Changes: filtered,
			})
		}
	}
	return result
}

// buildEmptySemanticDiffResponse returns the response shape for a no-changes diff.
func buildEmptySemanticDiffResponse() map[string]any {
	return map[string]any{
		"files":            []any{},
		"breaking_changes": []any{},
		"impact":           []any{},
		"summary": semdiff.DiffSummary{
			FilesChanged:    0,
			SymbolsAdded:    0,
			SymbolsRemoved:  0,
			SymbolsModified: 0,
			BreakingCount:   0,
			ImpactedCount:   0,
			HasCrossService: false,
		},
	}
}
