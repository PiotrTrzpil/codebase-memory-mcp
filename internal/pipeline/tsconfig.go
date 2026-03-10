package pipeline

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// tsconfigPathMap holds compiled path aliases from tsconfig.json.
type tsconfigPathMap struct {
	baseURL string              // relative to project root, e.g. "src" or "."
	paths   map[string][]string // "@components/*" → ["src/components/*"]
}

// loadTSConfig reads tsconfig.json from the project root (if it exists)
// and parses compilerOptions.baseUrl and compilerOptions.paths.
// Returns nil if tsconfig.json doesn't exist or has no relevant settings.
func loadTSConfig(repoPath string) *tsconfigPathMap {
	tsconfigPath := filepath.Join(repoPath, "tsconfig.json")
	data, err := os.ReadFile(tsconfigPath)
	if err != nil {
		return nil
	}

	var raw struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Warn("tsconfig.parse.err", "path", tsconfigPath, "err", err)
		return nil
	}

	// Nothing useful to extract
	if raw.CompilerOptions.BaseURL == "" && len(raw.CompilerOptions.Paths) == 0 {
		return nil
	}

	result := &tsconfigPathMap{
		baseURL: raw.CompilerOptions.BaseURL,
		paths:   raw.CompilerOptions.Paths,
	}

	if result.baseURL == "" {
		result.baseURL = "."
	}

	slog.Info("tsconfig.loaded", "baseUrl", result.baseURL, "aliases", len(result.paths))
	return result
}

// resolvePathAlias attempts to resolve a non-relative import path using
// tsconfig path aliases. Returns the resolved relative path (suitable for
// fqn.ModuleQN) or "" if no alias matched.
func (tc *tsconfigPathMap) resolvePathAlias(importPath string) string {
	if tc == nil {
		return ""
	}

	for pattern, replacements := range tc.paths {
		prefix, hasStar := strings.CutSuffix(pattern, "*")
		if !hasStar {
			// Exact match: "@utils" → ["src/utils"]
			if importPath == pattern && len(replacements) > 0 {
				return tc.buildResolved(replacements[0], "")
			}
			continue
		}

		// Wildcard match: "@components/*" matches "@components/Button"
		if !strings.HasPrefix(importPath, prefix) {
			continue
		}
		suffix := importPath[len(prefix):]

		for _, repl := range replacements {
			replPrefix, replHasStar := strings.CutSuffix(repl, "*")
			var resolved string
			if replHasStar {
				resolved = replPrefix + suffix
			} else {
				resolved = repl
			}
			return tc.buildResolved(resolved, "")
		}
	}

	// If baseUrl is set and no alias matched, the import might be relative to baseUrl.
	// e.g., baseUrl: "src" means `import X from "utils/foo"` → "src/utils/foo"
	if tc.baseURL != "" && tc.baseURL != "." {
		return filepath.ToSlash(filepath.Join(tc.baseURL, importPath))
	}

	return ""
}

// buildResolved joins the replacement path with baseURL and normalizes it.
func (tc *tsconfigPathMap) buildResolved(replacement, _ string) string {
	resolved := filepath.Join(tc.baseURL, replacement)
	resolved = filepath.Clean(resolved)
	resolved = filepath.ToSlash(resolved)

	// Strip known extensions
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx"} {
		resolved = strings.TrimSuffix(resolved, ext)
	}
	// Strip /index suffix
	resolved = strings.TrimSuffix(resolved, "/index")

	return resolved
}
