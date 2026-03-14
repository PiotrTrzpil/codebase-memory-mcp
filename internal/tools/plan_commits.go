package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/semdiff"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerPlanCommits() {
	s.addTool(&mcp.Tool{
		Name:        "plan_commits",
		Description: "Analyze changes and suggest how to split them into logical commits. Uses graph relationships to group coupled changes (e.g., signature change + caller updates). Produces compact, LLM-friendly output with draft messages. Minimizes context usage compared to raw git diff.",
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
				"project": {
					"type": "string",
					"description": "Project to analyze. Defaults to session project."
				}
			}
		}`),
	}, s.handlePlanCommits)
}

func (s *Server) handlePlanCommits(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	dp := parseDiffParams(args)

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

	if len(changedFiles) == 0 {
		plan := semdiff.CommitPlan{
			Commits: []semdiff.CommitGroup{},
			Stats:   semdiff.PlanStats{},
		}
		responseData := map[string]any{"plan": plan}
		s.addIndexStatus(responseData)
		result := s.result(responseData)
		s.addUpdateNotice(result)
		return result, nil
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

	oldDefMap := make(map[string]cbm.Definition)
	newDefMap := make(map[string]cbm.Definition)

	for _, f := range changedFiles {
		var oldDefs []cbm.Definition
		oldKey := f.Path
		if f.OldPath != "" {
			oldKey = f.OldPath
		}
		if oldContent, ok := oldContents[oldKey]; ok && len(oldContent) > 0 {
			l, ok := languageForFile(oldKey)
			if !ok {
				slog.Debug("plan_commits.skip_old_lang", "path", oldKey)
			} else {
				fr, err := cbm.ExtractFile(oldContent, l, projName, oldKey)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("parse old %s: %v", oldKey, err))
					slog.Warn("plan_commits.parse_old_err", "path", oldKey, "err", err)
				} else if fr != nil {
					oldDefs = fr.Definitions
					for _, d := range oldDefs {
						oldDefMap[d.QualifiedName] = d
					}
				}
			}
		}

		var newDefs []cbm.Definition
		if f.Status != "D" {
			newContent, newErr := readNewFileContent(repoPath, f.Path, dp.Scope)
			if newErr != nil {
				warnings = append(warnings, fmt.Sprintf("read new %s: %v", f.Path, newErr))
				slog.Warn("plan_commits.read_new_err", "path", f.Path, "err", newErr)
			} else if len(newContent) > 0 {
				l, ok := languageForFile(f.Path)
				if !ok {
					slog.Debug("plan_commits.skip_new_lang", "path", f.Path)
				} else {
					fr, err := cbm.ExtractFile(newContent, l, projName, f.Path)
					if err != nil {
						warnings = append(warnings, fmt.Sprintf("parse new %s: %v", f.Path, err))
						slog.Warn("plan_commits.parse_new_err", "path", f.Path, "err", err)
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

	// Classify breaking changes: mutates allChanges in-place (IsBreaking flag).
	// We then propagate IsBreaking back into allFileSummaries by QN, since
	// allChanges holds copies. PlanCommits uses IsBreaking from the file summaries.
	semdiff.ClassifyBreaking(allChanges, oldDefMap, newDefMap)
	isBreakingByQN := make(map[string]bool, len(allChanges))
	for _, c := range allChanges {
		if c.IsBreaking {
			isBreakingByQN[c.QualifiedName] = true
		}
	}
	for i := range allFileSummaries {
		for j := range allFileSummaries[i].Changes {
			if isBreakingByQN[allFileSummaries[i].Changes[j].QualifiedName] {
				allFileSummaries[i].Changes[j].IsBreaking = true
			}
		}
	}

	// Find coupling edges via graph relationships between changed symbols
	couplings := findCouplings(st, projName, allFileSummaries)

	// Build the commit plan using semdiff's grouping logic
	plan := semdiff.PlanCommits(allFileSummaries, couplings)

	responseData := map[string]any{"plan": plan}
	if len(warnings) > 0 {
		responseData["warnings"] = warnings
	}

	s.addIndexStatus(responseData)
	result := s.result(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

// findCouplings identifies graph-level relationships between changed symbols.
// When two changed symbols have a CALLS or USAGE edge between them, they are
// likely part of the same logical change and should land in the same commit.
func findCouplings(st *store.Store, projName string, fileSummaries []semdiff.FileChangeSummary) []semdiff.CouplingEdge {
	// Build set of changed qualified names for quick lookup
	changedQNs := map[string]bool{}
	for _, fs := range fileSummaries {
		for _, c := range fs.Changes {
			if c.QualifiedName != "" {
				changedQNs[c.QualifiedName] = true
			}
		}
	}

	var couplings []semdiff.CouplingEdge
	seen := map[string]bool{}

	for _, fs := range fileSummaries {
		nodes, err := st.FindNodesByFile(projName, fs.Path)
		if err != nil {
			slog.Debug("plan_commits.find_nodes.err", "path", fs.Path, "err", err)
			continue
		}

		for _, node := range nodes {
			if !changedQNs[node.QualifiedName] {
				continue
			}

			for _, edgeType := range []string{"CALLS", "USAGE"} {
				edges, err := st.FindEdgesBySourceAndType(node.ID, edgeType)
				if err != nil {
					slog.Debug("plan_commits.find_edges.err", "node", node.QualifiedName, "type", edgeType, "err", err)
					continue
				}

				for _, edge := range edges {
					targetNode, err := st.FindNodeByID(edge.TargetID)
					if err != nil || targetNode == nil {
						continue
					}
					if changedQNs[targetNode.QualifiedName] {
						key := node.QualifiedName + ":" + targetNode.QualifiedName
						if !seen[key] {
							seen[key] = true
							couplings = append(couplings, semdiff.CouplingEdge{
								FromQN: node.QualifiedName,
								ToQN:   targetNode.QualifiedName,
								Type:   edgeType,
							})
						}
					}
				}
			}
		}
	}

	return couplings
}
