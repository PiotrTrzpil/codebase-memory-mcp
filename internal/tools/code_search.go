package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type codeMatch struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Context []string `json:"context,omitempty"`
}

// searchCodeParams holds parsed parameters for a code search request.
type searchCodeParams struct {
	pattern       string
	fileGlob      string
	maxResults    int
	offset        int
	contextLines  int
	isRegex       bool
	caseSensitive bool
	re            *regexp.Regexp
	project       string
}

// parseSearchCodeParams extracts and validates search_code parameters from the request.
func parseSearchCodeParams(req *mcp.CallToolRequest) (*searchCodeParams, *mcp.CallToolResult) {
	args, err := parseArgs(req)
	if err != nil {
		return nil, errResult(err.Error())
	}

	contextLines := getIntArg(args, "context_lines", 2)
	if contextLines < 0 {
		contextLines = 0
	}
	if contextLines > 5 {
		contextLines = 5
	}

	p := &searchCodeParams{
		pattern:       getStringArg(args, "pattern"),
		fileGlob:      getStringArg(args, "file_pattern"),
		maxResults:    getIntArg(args, "max_results", 10),
		offset:        getIntArg(args, "offset", 0),
		contextLines:  contextLines,
		isRegex:       getBoolArg(args, "regex"),
		caseSensitive: getBoolArg(args, "case_sensitive"),
		project:       getStringArg(args, "project"),
	}

	if p.pattern == "" {
		return nil, errResult("pattern is required")
	}

	if p.isRegex {
		pattern := p.pattern
		if !p.caseSensitive && !strings.HasPrefix(pattern, "(?i)") {
			pattern = "(?i)" + pattern
		}
		p.re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, errResult(fmt.Sprintf("invalid regex: %v", err))
		}
	} else if !p.caseSensitive {
		// For literal mode, lowercase the pattern; matching done case-insensitively
		p.pattern = strings.ToLower(p.pattern)
	}

	return p, nil
}

func (s *Server) handleSearchCode(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	params, errRes := parseSearchCodeParams(req)
	if errRes != nil {
		return errRes, nil
	}

	// Resolve project root
	root, err := s.resolveProjectRoot(params.project)
	if err != nil {
		return errResult(fmt.Sprintf("resolve root: %v", err)), nil
	}

	filePaths := s.collectSearchFilePaths(params.fileGlob, params.project)

	// Two-pass approach: first count total matches, then collect the page with context.
	// Pass 1: count all matches across all files.
	totalMatches := 0
	type fileMatchInfo struct {
		relPath string
		absPath string
	}
	var matchFiles []fileMatchInfo
	for _, relPath := range filePaths {
		absPath := filepath.Join(root, relPath)
		n := countFileMatches(absPath, params.pattern, params.re, params.isRegex, params.caseSensitive)
		if n > 0 {
			totalMatches += n
			matchFiles = append(matchFiles, fileMatchInfo{relPath, absPath})
		}
	}

	// Pass 2: collect the requested page of results (with optional context lines).
	fetchLimit := params.offset + params.maxResults
	var allMatches []codeMatch
	for _, fi := range matchFiles {
		if len(allMatches) >= fetchLimit {
			break
		}
		fileMatches := searchFile(fi.absPath, fi.relPath, params.pattern, params.re, params.isRegex, params.caseSensitive, fetchLimit-len(allMatches), params.contextLines)
		allMatches = append(allMatches, fileMatches...)
	}

	total := len(allMatches)

	// Apply offset and limit
	start := params.offset
	if start > total {
		start = total
	}
	end := start + params.maxResults
	if end > total {
		end = total
	}
	pageMatches := allMatches[start:end]

	responseData := map[string]any{
		"pattern":       params.pattern,
		"total_matches": totalMatches,
		"limit":         params.maxResults,
		"offset":        params.offset,
		"has_more":      params.offset+params.maxResults < totalMatches,
		"matches":       pageMatches,
		"files_count":   len(filePaths),
	}
	s.addIndexStatus(responseData)

	result := s.result(responseData)
	s.addUpdateNotice(result)
	return result, nil
}

// collectSearchFilePaths gathers indexed file paths, optionally filtered by a glob pattern.
func (s *Server) collectSearchFilePaths(fileGlob, project string) []string {
	var filePaths []string

	collectFromStore := func(st *store.Store, projName string) {
		files, _ := st.FindNodesByLabel(projName, "File")
		for _, f := range files {
			if f.FilePath == "" {
				continue
			}
			if fileGlob != "" {
				matched, _ := filepath.Match(fileGlob, filepath.Base(f.FilePath))
				if !matched {
					matched = globMatch(fileGlob, f.FilePath)
				}
				if !matched {
					continue
				}
			}
			filePaths = append(filePaths, f.FilePath)
		}
	}

	st, err := s.resolveStore(project)
	if err != nil {
		return filePaths
	}

	projName := s.resolveProjectName(project)
	projects, _ := st.ListProjects()
	if len(projects) > 0 {
		projName = projects[0].Name
	}
	collectFromStore(st, projName)

	return filePaths
}

// matchLine checks whether a single line matches the search criteria.
func matchLine(line, pattern string, re *regexp.Regexp, isRegex, caseSensitive bool) bool {
	switch {
	case isRegex:
		return re.MatchString(line)
	case caseSensitive:
		return strings.Contains(line, pattern)
	default:
		return strings.Contains(strings.ToLower(line), pattern)
	}
}

// countFileMatches returns the total number of matching lines in a file without
// collecting results. Used for accurate total_matches counts.
func countFileMatches(absPath, pattern string, re *regexp.Regexp, isRegex, caseSensitive bool) int {
	f, err := os.Open(absPath)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if matchLine(scanner.Text(), pattern, re, isRegex, caseSensitive) {
			count++
		}
	}
	return count
}

func searchFile(absPath, relPath, pattern string, re *regexp.Regexp, isRegex, caseSensitive bool, limit, contextLines int) []codeMatch {
	f, err := os.Open(absPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	// When context is requested, read all lines first so we can look ahead/behind.
	if contextLines > 0 {
		return searchFileWithContext(f, relPath, pattern, re, isRegex, caseSensitive, limit, contextLines)
	}

	var matches []codeMatch
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if matchLine(line, pattern, re, isRegex, caseSensitive) {
			content := strings.TrimSpace(line)
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			matches = append(matches, codeMatch{
				File:    relPath,
				Line:    lineNum,
				Content: content,
			})
			if len(matches) >= limit {
				break
			}
		}
	}

	return matches
}

// searchFileWithContext reads the full file into memory and returns matches
// with surrounding context lines (before + after).
func searchFileWithContext(f *os.File, relPath, pattern string, re *regexp.Regexp, isRegex, caseSensitive bool, limit, contextLines int) []codeMatch {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	var matches []codeMatch
	for i, line := range lines {
		if !matchLine(line, pattern, re, isRegex, caseSensitive) {
			continue
		}

		content := strings.TrimSpace(line)
		if len(content) > 200 {
			content = content[:200] + "..."
		}

		// Gather context: before and after lines
		ctxStart := i - contextLines
		if ctxStart < 0 {
			ctxStart = 0
		}
		ctxEnd := i + contextLines
		if ctxEnd >= len(lines) {
			ctxEnd = len(lines) - 1
		}

		ctx := make([]string, 0, ctxEnd-ctxStart+1)
		for j := ctxStart; j <= ctxEnd; j++ {
			if j == i {
				continue // skip the match line itself, it's in Content
			}
			prefix := " "
			cl := lines[j]
			if len(cl) > 200 {
				cl = cl[:200] + "..."
			}
			ctx = append(ctx, fmt.Sprintf("%s%d: %s", prefix, j+1, cl))
		}

		matches = append(matches, codeMatch{
			File:    relPath,
			Line:    i + 1,
			Content: content,
			Context: ctx,
		})
		if len(matches) >= limit {
			break
		}
	}

	return matches
}

// globMatch does a simple glob match supporting ** patterns.
func globMatch(pattern, path string) bool {
	if strings.Contains(pattern, "**") {
		// Split pattern on **
		parts := strings.SplitN(pattern, "**", 2)
		prefix := strings.TrimRight(parts[0], "/")
		suffix := strings.TrimLeft(parts[1], "/")

		if prefix != "" && !strings.HasPrefix(path, prefix) {
			return false
		}
		if suffix != "" {
			matched, _ := filepath.Match(suffix, filepath.Base(path))
			return matched
		}
		return true
	}
	matched, _ := filepath.Match(pattern, path)
	return matched
}
