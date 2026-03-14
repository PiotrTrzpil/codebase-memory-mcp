package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/DeusData/codebase-memory-mcp/internal/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

func main() {
	tools.SetVersion(version)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println("codebase-memory-mcp", version)
			os.Exit(0)
		case "install":
			os.Exit(runInstall(os.Args[2:]))
		case "uninstall":
			os.Exit(runUninstall(os.Args[2:]))
		case "update":
			os.Exit(runUpdate(os.Args[2:]))
		case "cli":
			if len(os.Args) >= 3 {
				os.Exit(runCLI(os.Args[2:]))
			}
		}
	}

	router, err := store.NewRouter()
	if err != nil {
		log.Fatalf("store router err=%v", err)
	}

	srv := tools.NewServer(router)

	ctx, cancel := context.WithCancel(context.Background())
	srv.StartWatcher(ctx)

	runErr := srv.MCPServer().Run(ctx, &mcp.StdioTransport{})
	cancel()
	router.CloseAll()
	if runErr != nil {
		log.Fatalf("server err=%v", runErr)
	}
}

func runCLI(args []string) int {
	// Parse flags
	raw := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--raw":
			raw = true
		default:
			positional = append(positional, a)
		}
	}

	router, err := store.NewRouter()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer router.CloseAll()

	if len(positional) == 0 || positional[0] == "--help" || positional[0] == "-h" {
		srv := tools.NewServer(router)
		fmt.Fprintf(os.Stderr, "Usage: codebase-memory-mcp cli [--raw] <tool_name> [json_args]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n  --raw    Print full JSON output (default: human-friendly summary)\n\n")
		fmt.Fprintf(os.Stderr, "Available tools:\n  %s\n", strings.Join(srv.ToolNames(), "\n  "))
		return 0
	}

	toolName := positional[0]

	srv := tools.NewServer(router)

	// In CLI mode, try to set session root from cwd
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		srv.SetSessionRoot(cwd)
	}

	var argsJSON json.RawMessage
	if len(positional) > 1 {
		argsJSON = json.RawMessage(positional[1])
	}

	result, err := srv.CallTool(context.Background(), toolName, argsJSON)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if result.IsError {
		for _, c := range result.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				fmt.Fprintf(os.Stderr, "error: %s\n", tc.Text)
			}
		}
		return 1
	}

	// Extract the text content
	var text string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text = tc.Text
			break
		}
	}

	if raw {
		printRawJSON(text)
		return 0
	}

	// Summary mode (default): print a human-friendly summary
	dbPath := filepath.Join(router.Dir(), srv.SessionProject()+".db")
	printSummary(toolName, text, dbPath)
	return 0
}

// printRawJSON pretty-prints JSON text to stdout.
func printRawJSON(text string) {
	var buf json.RawMessage
	if json.Unmarshal([]byte(text), &buf) == nil {
		if pretty, err := json.MarshalIndent(buf, "", "  "); err == nil {
			fmt.Println(string(pretty))
			return
		}
	}
	fmt.Println(text)
}

// printSummary prints a human-friendly summary of the tool result.
func printSummary(toolName, text, dbPath string) {
	var data map[string]any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		// Not a JSON object — might be an array (e.g. list_projects)
		var arr []any
		if err2 := json.Unmarshal([]byte(text), &arr); err2 == nil {
			printArraySummary(toolName, arr, dbPath)
			return
		}
		// Plain text — print as-is
		fmt.Println(text)
		return
	}

	switch toolName {
	case "index_repository":
		printIndexSummary(data, dbPath)
	case "search_graph":
		printSearchGraphSummary(data)
	case "search_code":
		printSearchCodeSummary(data)
	case "trace_call_path":
		printTraceSummary(data)
	case "query_graph":
		printQuerySummary(data)
	case "get_graph_schema":
		printSchemaSummary(data)
	case "get_code_snippet":
		printSnippetSummary(data)
	case "delete_project":
		printDeleteSummary(data)
	case "read_file":
		printReadFileSummary(data)
	case "list_directory":
		printListDirSummary(data)
	case "ingest_traces":
		printIngestSummary(data, dbPath)
	case "index_status":
		printIndexStatusSummary(data)
	case "detect_changes":
		printDetectChangesSummary(data)
	case "get_architecture":
		printArchitectureSummary(data)
	case "manage_adr":
		printADRSummary(data)
	default:
		// Fallback: pretty-print the JSON
		printRawJSON(text)
	}
}

func printArraySummary(toolName string, arr []any, dbPath string) {
	switch toolName {
	case "list_projects":
		if len(arr) == 0 {
			fmt.Println("No projects indexed.")
			fmt.Printf("  db_dir: %s\n", filepath.Dir(dbPath))
			return
		}
		fmt.Printf("%d project(s) indexed:\n", len(arr))
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			nodes := jsonInt(m["nodes"])
			edges := jsonInt(m["edges"])
			indexedAt, _ := m["indexed_at"].(string)
			rootPath, _ := m["root_path"].(string)
			isSession, _ := m["is_session_project"].(bool)
			sessionMarker := ""
			if isSession {
				sessionMarker = " *"
			}
			fmt.Printf("  %-30s %d nodes, %d edges  (indexed %s)%s\n", name, nodes, edges, indexedAt, sessionMarker)
			if rootPath != "" {
				fmt.Printf("  %-30s %s\n", "", rootPath)
			}
			if dbp, ok := m["db_path"].(string); ok {
				fmt.Printf("  %-30s %s\n", "", dbp)
			}
		}
	default:
		fmt.Printf("%d result(s)\n", len(arr))
		printRawJSON(mustJSON(arr))
	}
}

func printIndexSummary(data map[string]any, dbPath string) {
	project, _ := data["project"].(string)
	nodes := jsonInt(data["nodes"])
	edges := jsonInt(data["edges"])
	indexedAt, _ := data["indexed_at"].(string)
	fmt.Printf("Indexed %q: %d nodes, %d edges\n", project, nodes, edges)
	fmt.Printf("  indexed_at: %s\n", indexedAt)
	fmt.Printf("  db: %s\n", dbPath)
}

func printSearchGraphSummary(data map[string]any) {
	total := jsonInt(data["total"])
	hasMore, _ := data["has_more"].(bool)
	results, _ := data["results"].([]any)
	shown := len(results)

	fmt.Printf("%d result(s) found", total)
	if hasMore {
		fmt.Printf(" (showing %d, has_more=true)", shown)
	}
	fmt.Println()

	for _, r := range results {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		label, _ := m["label"].(string)
		filePath, _ := m["file_path"].(string)
		startLine := jsonInt(m["start_line"])
		fmt.Printf("  [%s] %s", label, name)
		if filePath != "" {
			fmt.Printf("  %s:%d", filePath, startLine)
		}
		// Degree counts
		inDeg := jsonInt(m["in_degree"])
		outDeg := jsonInt(m["out_degree"])
		if inDeg > 0 || outDeg > 0 {
			fmt.Printf("\n         in=%d out=%d", inDeg, outDeg)
		}
		fmt.Println()
		// Connected names (from include_connected=true)
		if connected, ok := m["connected_names"].([]any); ok && len(connected) > 0 {
			names := make([]string, 0, len(connected))
			for _, c := range connected {
				if s, ok := c.(string); ok {
					names = append(names, s)
				}
			}
			fmt.Printf("         connected: %s\n", strings.Join(names, ", "))
		}
	}
}

func printSearchCodeSummary(data map[string]any) {
	total := jsonInt(data["total_matches"])
	if total == 0 {
		total = jsonInt(data["total"]) // backwards compat
	}
	hasMore, _ := data["has_more"].(bool)
	matches, _ := data["matches"].([]any)
	shown := len(matches)

	fmt.Printf("%d match(es) found", total)
	if hasMore {
		fmt.Printf(" (showing %d, has_more=true)", shown)
	}
	fmt.Println()

	for _, m := range matches {
		if entry, ok := m.(map[string]any); ok {
			file, _ := entry["file"].(string)
			line := jsonInt(entry["line"])
			content, _ := entry["content"].(string)
			fmt.Printf("  %s:%d  %s\n", file, line, content)
			// Print context lines if present
			if ctx, ok := entry["context"].([]any); ok && len(ctx) > 0 {
				for _, c := range ctx {
					if s, ok := c.(string); ok {
						fmt.Printf("       %s\n", s)
					}
				}
			}
		}
	}
}

func printTraceSummary(data map[string]any) {
	root, _ := data["root"].(map[string]any)
	rootName, _ := root["name"].(string)
	hops, _ := data["hops"].([]any)

	// Handle summary_only mode
	isSummary, _ := data["summary_only"].(bool)
	if isSummary {
		totalNodes := jsonInt(data["total_nodes"])
		totalEdges := jsonInt(data["total_edges"])
		fmt.Printf("Trace from %q: %d node(s), %d edge(s), %d hop(s)  [summary]\n", rootName, totalNodes, totalEdges, len(hops))
	} else {
		totalResults := jsonInt(data["total_results"])
		edges, _ := data["edges"].([]any)
		fmt.Printf("Trace from %q: %d node(s), %d edge(s), %d hop(s)\n", rootName, totalResults, len(edges), len(hops))
	}

	// Root node details
	if root != nil {
		if label, ok := root["label"].(string); ok {
			fmt.Printf("  root: [%s] %s", label, rootName)
			if fp, ok := root["file_path"].(string); ok && fp != "" {
				fmt.Printf("  %s:%d", fp, jsonInt(root["start_line"]))
			}
			fmt.Println()
		}
		if sig, ok := root["signature"].(string); ok && sig != "" {
			fmt.Printf("    sig: %s\n", sig)
		}
		if rt, ok := root["return_type"].(string); ok && rt != "" {
			fmt.Printf("    return_type: %s\n", rt)
		}
	}

	// Impact summary (when risk_labels=true)
	if impact, ok := data["impact_summary"].(map[string]any); ok {
		critical := jsonInt(impact["critical"])
		high := jsonInt(impact["high"])
		medium := jsonInt(impact["medium"])
		low := jsonInt(impact["low"])
		impactTotal := jsonInt(impact["total"])
		fmt.Printf("  impact: %d total — CRITICAL: %d  HIGH: %d  MEDIUM: %d  LOW: %d\n", impactTotal, critical, high, medium, low)
		if crossSvc, ok := impact["has_cross_service"].(bool); ok && crossSvc {
			fmt.Printf("  ⚠ has cross-service edges\n")
		}
	}

	// Edge type distribution (summary_only mode)
	if isSummary {
		if edgeTypes, ok := data["edge_types"].(map[string]any); ok && len(edgeTypes) > 0 {
			fmt.Printf("  edge types:")
			for et, count := range edgeTypes {
				fmt.Printf("  %s: %d", et, jsonInt(count))
			}
			fmt.Println()
		}
	}

	for _, h := range hops {
		if hop, ok := h.(map[string]any); ok {
			hopNum := jsonInt(hop["hop"])
			// Summary mode: hops have "count" instead of "nodes"
			if isSummary {
				count := jsonInt(hop["count"])
				fmt.Printf("  hop %d: %d node(s)\n", hopNum, count)
				continue
			}
			nodes, _ := hop["nodes"].([]any)
			fmt.Printf("  hop %d: %d node(s)\n", hopNum, len(nodes))
			for _, n := range nodes {
				if nm, ok := n.(map[string]any); ok {
					name, _ := nm["name"].(string)
					label, _ := nm["label"].(string)
					line := fmt.Sprintf("    [%s] %s", label, name)
					if risk, ok := nm["risk"].(string); ok && risk != "" {
						line += fmt.Sprintf("  [%s]", risk)
					}
					if sig, ok := nm["signature"].(string); ok && sig != "" {
						line += fmt.Sprintf("  sig: %s", sig)
					}
					fmt.Println(line)
				}
			}
		}
	}

	edgesArr, _ := data["edges"].([]any)
	if len(edgesArr) > 0 {
		fmt.Println("  edges:")
		for _, e := range edgesArr {
			if edge, ok := e.(map[string]any); ok {
				from, _ := edge["from"].(string)
				to, _ := edge["to"].(string)
				edgeType, _ := edge["type"].(string)
				line := fmt.Sprintf("    %s → %s [%s]", from, to, edgeType)
				if conf, ok := edge["confidence"].(float64); ok && conf > 0 {
					band, _ := edge["confidence_band"].(string)
					strategy, _ := edge["resolution_strategy"].(string)
					line += fmt.Sprintf("  confidence=%.2f", conf)
					if band != "" {
						line += fmt.Sprintf(" (%s", band)
						if strategy != "" {
							line += fmt.Sprintf(", %s", strategy)
						}
						line += ")"
					}
				}
				fmt.Println(line)
			}
		}
	}
}

func printQuerySummary(data map[string]any) {
	total := jsonInt(data["total"])
	columns, _ := data["columns"].([]any)
	rows, _ := data["rows"].([]any)

	colNames := make([]string, len(columns))
	for i, c := range columns {
		colNames[i], _ = c.(string)
	}

	fmt.Printf("%d row(s) returned", total)
	if len(colNames) > 0 {
		fmt.Printf("  [%s]", strings.Join(colNames, ", "))
	}
	fmt.Println()

	for _, row := range rows {
		switch r := row.(type) {
		case map[string]any:
			// Rows are maps keyed by column name
			parts := make([]string, len(colNames))
			for i, col := range colNames {
				parts[i] = fmt.Sprintf("%v", r[col])
			}
			fmt.Printf("  %s\n", strings.Join(parts, " | "))
		case []any:
			parts := make([]string, len(r))
			for i, v := range r {
				parts[i] = fmt.Sprintf("%v", v)
			}
			fmt.Printf("  %s\n", strings.Join(parts, " | "))
		}
	}
}

func printSchemaSummary(data map[string]any) {
	projects, _ := data["projects"].([]any)
	if len(projects) == 0 {
		fmt.Println("No projects indexed.")
		return
	}

	for _, p := range projects {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		projName, _ := pm["project"].(string)
		schema, _ := pm["schema"].(map[string]any)
		if schema == nil {
			continue
		}

		fmt.Printf("Project: %s\n", projName)
		if labels, ok := schema["node_labels"].([]any); ok {
			fmt.Printf("  Node labels (%d):\n", len(labels))
			for _, l := range labels {
				if lm, ok := l.(map[string]any); ok {
					label, _ := lm["label"].(string)
					count := jsonInt(lm["count"])
					fmt.Printf("    %-15s %d\n", label, count)
				}
			}
		}
		if rels, ok := schema["relationship_types"].([]any); ok {
			fmt.Printf("  Edge types (%d):\n", len(rels))
			for _, r := range rels {
				if rm, ok := r.(map[string]any); ok {
					relType, _ := rm["type"].(string)
					count := jsonInt(rm["count"])
					fmt.Printf("    %-25s %d\n", relType, count)
				}
			}
		}
	}
}

func printSnippetSummary(data map[string]any) {
	name, _ := data["name"].(string)
	label, _ := data["label"].(string)
	filePath, _ := data["file_path"].(string)
	startLine := jsonInt(data["start_line"])
	endLine := jsonInt(data["end_line"])
	source, _ := data["source"].(string)

	fmt.Printf("[%s] %s  (%s:%d-%d)\n", label, name, filePath, startLine, endLine)
	if sig, ok := data["signature"].(string); ok && sig != "" {
		fmt.Printf("  signature: %s\n", sig)
	}
	if rt, ok := data["return_type"].(string); ok && rt != "" {
		fmt.Printf("  return_type: %s\n", rt)
	}
	if complexity, ok := data["complexity"]; ok {
		fmt.Printf("  complexity: %v\n", complexity)
	}
	if doc, ok := data["docstring"].(string); ok && doc != "" {
		fmt.Printf("  docstring: %s\n", doc)
	}
	callers := jsonInt(data["callers"])
	callees := jsonInt(data["callees"])
	if callers > 0 || callees > 0 {
		fmt.Printf("  callers=%d callees=%d\n", callers, callees)
	}
	fmt.Println()
	fmt.Println(source)
}

func printDeleteSummary(data map[string]any) {
	deleted, _ := data["deleted"].(string)
	fmt.Printf("Deleted project %q\n", deleted)
}

func printReadFileSummary(data map[string]any) {
	path, _ := data["path"].(string)
	totalLines := jsonInt(data["total_lines"])
	content, _ := data["content"].(string)

	fmt.Printf("%s (%d lines)\n\n", path, totalLines)
	fmt.Println(content)
}

func printListDirSummary(data map[string]any) {
	dir, _ := data["directory"].(string)
	count := jsonInt(data["count"])
	entries, _ := data["entries"].([]any)

	fmt.Printf("%s (%d entries)\n", dir, count)
	for _, e := range entries {
		if em, ok := e.(map[string]any); ok {
			name, _ := em["name"].(string)
			isDir, _ := em["is_dir"].(bool)
			if isDir {
				fmt.Printf("  %s/\n", name)
			} else {
				size := jsonInt(em["size"])
				fmt.Printf("  %-40s %d bytes\n", name, size)
			}
		}
	}
}

func printIngestSummary(data map[string]any, dbPath string) {
	matched := jsonInt(data["matched"])
	boosted := jsonInt(data["boosted"])
	total := jsonInt(data["total_spans"])
	fmt.Printf("Ingested %d span(s): %d matched, %d boosted\n", total, matched, boosted)
	fmt.Printf("  db: %s\n", dbPath)
}

func printIndexStatusSummary(data map[string]any) {
	project, _ := data["project"].(string)
	status, _ := data["status"].(string)

	switch status {
	case "no_session":
		msg, _ := data["message"].(string)
		fmt.Println(msg)
	case "not_indexed":
		fmt.Printf("Project %q: not indexed\n", project)
		if dbPath, ok := data["db_path"].(string); ok {
			fmt.Printf("  expected db: %s\n", dbPath)
		}
	case "partial":
		fmt.Printf("Project %q: partially indexed (metadata missing)\n", project)
	case "indexing":
		fmt.Printf("Project %q: indexing in progress\n", project)
		if elapsed, ok := data["index_elapsed_seconds"]; ok {
			fmt.Printf("  elapsed: %ds\n", jsonInt(elapsed))
		}
		if indexType, ok := data["index_type"].(string); ok {
			fmt.Printf("  type: %s\n", indexType)
		}
	case "ready":
		nodes := jsonInt(data["nodes"])
		edges := jsonInt(data["edges"])
		indexedAt, _ := data["indexed_at"].(string)
		indexType, _ := data["index_type"].(string)
		isSession, _ := data["is_session_project"].(bool)
		fmt.Printf("Project %q: ready (%d nodes, %d edges)\n", project, nodes, edges)
		fmt.Printf("  indexed_at: %s\n", indexedAt)
		fmt.Printf("  index_type: %s\n", indexType)
		if isSession {
			fmt.Printf("  session_project: true\n")
		}
		if dbPath, ok := data["db_path"].(string); ok {
			fmt.Printf("  db: %s\n", dbPath)
		}
	default:
		printRawJSON(mustJSON(data))
	}
}

func printDetectChangesSummary(data map[string]any) {
	summary, _ := data["summary"].(map[string]any)
	changedFiles := jsonInt(summary["changed_files"])
	changedSymbols := jsonInt(summary["changed_symbols"])
	total := jsonInt(summary["total"])
	critical := jsonInt(summary["critical"])
	high := jsonInt(summary["high"])
	medium := jsonInt(summary["medium"])
	low := jsonInt(summary["low"])

	fmt.Printf("Changes: %d file(s), %d symbol(s) modified\n", changedFiles, changedSymbols)
	fmt.Printf("Impact: %d affected symbol(s)\n", total)
	if total > 0 {
		fmt.Printf("  CRITICAL: %d  HIGH: %d  MEDIUM: %d  LOW: %d\n", critical, high, medium, low)
	}

	impacted, _ := data["impacted_symbols"].([]any)
	for _, is := range impacted {
		m, ok := is.(map[string]any)
		if !ok {
			continue
		}
		risk, _ := m["risk"].(string)
		name, _ := m["name"].(string)
		label, _ := m["label"].(string)
		changedBy, _ := m["changed_by"].(string)
		fmt.Printf("  [%s] [%s] %s  (via %s)\n", risk, label, name, changedBy)
	}
}

func printArchitectureSummary(data map[string]any) {
	project, _ := data["project"].(string)
	fmt.Printf("Architecture: %s\n", project)

	if langs, ok := data["languages"].([]any); ok && len(langs) > 0 {
		fmt.Printf("  languages (%d):\n", len(langs))
		for _, l := range langs {
			if lm, ok := l.(map[string]any); ok {
				lang, _ := lm["language"].(string)
				count := jsonInt(lm["file_count"])
				fmt.Printf("    %-20s %d files\n", lang, count)
			}
		}
	}

	if pkgs, ok := data["packages"].([]any); ok && len(pkgs) > 0 {
		fmt.Printf("  packages (%d):\n", len(pkgs))
		for _, p := range pkgs {
			if pm, ok := p.(map[string]any); ok {
				name, _ := pm["name"].(string)
				nodes := jsonInt(pm["node_count"])
				fanIn := jsonInt(pm["fan_in"])
				fanOut := jsonInt(pm["fan_out"])
				fmt.Printf("    %-20s %d nodes  fan_in=%d fan_out=%d\n", name, nodes, fanIn, fanOut)
			}
		}
	}

	if eps, ok := data["entry_points"].([]any); ok && len(eps) > 0 {
		fmt.Printf("  entry_points (%d):\n", len(eps))
		for _, e := range eps {
			if em, ok := e.(map[string]any); ok {
				name, _ := em["name"].(string)
				file, _ := em["file"].(string)
				fmt.Printf("    %s  %s\n", name, file)
			}
		}
	}

	if routes, ok := data["routes"].([]any); ok && len(routes) > 0 {
		fmt.Printf("  routes (%d):\n", len(routes))
		for _, r := range routes {
			if rm, ok := r.(map[string]any); ok {
				method, _ := rm["method"].(string)
				path, _ := rm["path"].(string)
				handler, _ := rm["handler"].(string)
				fmt.Printf("    %-6s %-30s → %s\n", method, path, handler)
			}
		}
	}

	if hotspots, ok := data["hotspots"].([]any); ok && len(hotspots) > 0 {
		fmt.Printf("  hotspots (%d):\n", len(hotspots))
		for _, h := range hotspots {
			if hm, ok := h.(map[string]any); ok {
				name, _ := hm["name"].(string)
				fanIn := jsonInt(hm["fan_in"])
				fmt.Printf("    %-30s fan_in=%d\n", name, fanIn)
			}
		}
	}

	if boundaries, ok := data["boundaries"].([]any); ok && len(boundaries) > 0 {
		fmt.Printf("  boundaries (%d):\n", len(boundaries))
		for _, b := range boundaries {
			if bm, ok := b.(map[string]any); ok {
				from, _ := bm["from"].(string)
				to, _ := bm["to"].(string)
				count := jsonInt(bm["call_count"])
				fmt.Printf("    %s → %s  %d calls\n", from, to, count)
			}
		}
	}

	if services, ok := data["services"].([]any); ok && len(services) > 0 {
		fmt.Printf("  services (%d):\n", len(services))
		for _, s := range services {
			if sm, ok := s.(map[string]any); ok {
				from, _ := sm["from"].(string)
				to, _ := sm["to"].(string)
				stype, _ := sm["type"].(string)
				count := jsonInt(sm["count"])
				fmt.Printf("    %s → %s [%s]  %d\n", from, to, stype, count)
			}
		}
	}

	if layers, ok := data["layers"].([]any); ok && len(layers) > 0 {
		fmt.Printf("  layers (%d):\n", len(layers))
		for _, l := range layers {
			if lm, ok := l.(map[string]any); ok {
				name, _ := lm["name"].(string)
				layer, _ := lm["layer"].(string)
				reason, _ := lm["reason"].(string)
				fmt.Printf("    %-20s %-10s %s\n", name, layer, reason)
			}
		}
	}

	if clusters, ok := data["clusters"].([]any); ok && len(clusters) > 0 {
		fmt.Printf("  clusters (%d):\n", len(clusters))
		for _, c := range clusters {
			if cm, ok := c.(map[string]any); ok {
				id := jsonInt(cm["id"])
				label, _ := cm["label"].(string)
				members := jsonInt(cm["members"])
				cohesion, _ := cm["cohesion"].(float64)
				fmt.Printf("    #%d %-25s %d members  cohesion=%.2f\n", id, label, members, cohesion)
				if topNodes, ok := cm["top_nodes"].([]any); ok && len(topNodes) > 0 {
					names := make([]string, 0, len(topNodes))
					for _, tn := range topNodes {
						if s, ok := tn.(string); ok {
							names = append(names, s)
						}
					}
					fmt.Printf("       top: %s\n", strings.Join(names, ", "))
				}
			}
		}
	}

	if ft, ok := data["file_tree"].([]any); ok && len(ft) > 0 {
		fmt.Printf("  file_tree (%d entries):\n", len(ft))
		for _, f := range ft {
			if fm, ok := f.(map[string]any); ok {
				path, _ := fm["path"].(string)
				ftype, _ := fm["type"].(string)
				children := jsonInt(fm["children"])
				if ftype == "dir" {
					fmt.Printf("    %s/  (%d children)\n", path, children)
				} else {
					fmt.Printf("    %s\n", path)
				}
			}
		}
	}

	if adr, ok := data["adr"].(map[string]any); ok {
		updatedAt, _ := adr["updated_at"].(string)
		fmt.Printf("  adr: (updated %s)\n", updatedAt)
	} else if hint, ok := data["adr_hint"].(string); ok {
		fmt.Printf("  adr: %s\n", hint)
	}
}

func printADRSummary(data map[string]any) {
	project, _ := data["project"].(string)
	status, _ := data["status"].(string)

	if status != "" {
		fmt.Printf("ADR %s: %s\n", project, status)
		if updatedAt, ok := data["updated_at"].(string); ok {
			fmt.Printf("  updated_at: %s\n", updatedAt)
		}
		return
	}

	// mode=get response
	if hint, ok := data["adr_hint"].(string); ok {
		fmt.Printf("ADR %s: %s\n", project, hint)
		return
	}

	if text, ok := data["text"].(string); ok && text != "" {
		updatedAt, _ := data["updated_at"].(string)
		fmt.Printf("ADR %s (updated %s):\n\n%s\n", project, updatedAt, text)
	} else if sections, ok := data["sections"].(map[string]any); ok {
		updatedAt, _ := data["updated_at"].(string)
		fmt.Printf("ADR %s (updated %s):\n", project, updatedAt)
		for name, content := range sections {
			fmt.Printf("\n## %s\n%v\n", name, content)
		}
	}
}

// jsonInt extracts an integer from a JSON-decoded value (float64 or int).
func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
