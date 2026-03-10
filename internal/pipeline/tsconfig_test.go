package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTSConfig(t *testing.T) {
	dir := t.TempDir()

	// Write a tsconfig.json with path aliases
	tsconfig := `{
  "compilerOptions": {
    "baseUrl": "src",
    "paths": {
      "@components/*": ["components/*"],
      "@utils/*": ["utils/*", "shared/utils/*"],
      "@config": ["config/index"]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(tsconfig), 0644); err != nil {
		t.Fatal(err)
	}

	tc := loadTSConfig(dir)
	if tc == nil {
		t.Fatal("expected tsconfig to be loaded")
	}
	if tc.baseURL != "src" {
		t.Errorf("baseURL = %q, want %q", tc.baseURL, "src")
	}
	if len(tc.paths) != 3 {
		t.Errorf("paths count = %d, want 3", len(tc.paths))
	}
}

func TestLoadTSConfigMissing(t *testing.T) {
	tc := loadTSConfig(t.TempDir())
	if tc != nil {
		t.Error("expected nil for missing tsconfig.json")
	}
}

func TestResolvePathAlias(t *testing.T) {
	tc := &tsconfigPathMap{
		baseURL: "src",
		paths: map[string][]string{
			"@components/*": {"components/*"},
			"@utils/*":      {"utils/*", "shared/utils/*"},
			"@config":       {"config/index"},
		},
	}

	tests := []struct {
		importPath string
		want       string
	}{
		{"@components/Button", "src/components/Button"},
		{"@components/forms/Input", "src/components/forms/Input"},
		{"@utils/helpers", "src/utils/helpers"},
		{"@config", "src/config"},       // exact match, /index stripped
		{"react", "src/react"},           // no alias match, but baseUrl applies
		// Note: relative paths (./foo, ../bar) are handled before resolvePathAlias
		// is called, so they won't reach this function in practice.
	}

	for _, tt := range tests {
		t.Run(tt.importPath, func(t *testing.T) {
			got := tc.resolvePathAlias(tt.importPath)
			if got != tt.want {
				t.Errorf("resolvePathAlias(%q) = %q, want %q", tt.importPath, got, tt.want)
			}
		})
	}
}

func TestResolvePathAliasNil(t *testing.T) {
	var tc *tsconfigPathMap
	if got := tc.resolvePathAlias("@components/Foo"); got != "" {
		t.Errorf("expected empty string for nil tsconfig, got %q", got)
	}
}

func TestResolvePathAliasDotBaseURL(t *testing.T) {
	tc := &tsconfigPathMap{
		baseURL: ".",
		paths: map[string][]string{
			"@/*": {"src/*"},
		},
	}

	got := tc.resolvePathAlias("@/components/Button")
	if got != "src/components/Button" {
		t.Errorf("resolvePathAlias(@/components/Button) = %q, want %q", got, "src/components/Button")
	}

	// With baseUrl ".", non-matching aliases should return "" (no baseUrl prefix to add)
	got = tc.resolvePathAlias("lodash")
	if got != "" {
		t.Errorf("resolvePathAlias(lodash) = %q, want empty (baseUrl is '.')", got)
	}
}
