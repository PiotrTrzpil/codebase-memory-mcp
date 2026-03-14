package pipeline

import (
	"log/slog"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/DeusData/codebase-memory-mcp/internal/fqn"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
)

// passUsesType creates USES_TYPE edges using pre-extracted CBM data.
func (p *Pipeline) passUsesType() {
	slog.Info("pass.usestype")

	type fileEntry struct {
		relPath string
		ext     *cachedExtraction
	}
	var files []fileEntry
	for relPath, ext := range p.extractionCache {
		if lang.ForLanguage(ext.Language) != nil && len(ext.Result.TypeRefs) > 0 {
			files = append(files, fileEntry{relPath, ext})
		}
	}

	if len(files) == 0 {
		return
	}

	// Stage 1: Parallel per-file type reference resolution using CBM data
	results := make([][]resolvedEdge, len(files))
	numWorkers := runtime.NumCPU()
	if numWorkers > len(files) {
		numWorkers = len(files)
	}

	g := new(errgroup.Group)
	g.SetLimit(numWorkers)
	for i, fe := range files {
		g.Go(func() error {
			results[i] = p.resolveFileTypeRefsCBM(fe.relPath, fe.ext)
			return nil
		})
	}
	_ = g.Wait()

	// Stage 2: Batch write
	p.flushResolvedEdges(results)

	total := 0
	for _, r := range results {
		total += len(r)
	}
	slog.Info("pass.usestype.done", "edges", total)
}

// passParamTypeEdges creates USES_TYPE edges from function/method param_types
// and return_type properties. This captures type relationships not found by
// tree-sitter's AST-based type reference extraction (e.g. TypeScript interface
// and type alias usage in function signatures).
func (p *Pipeline) passParamTypeEdges() {
	slog.Info("pass.paramtype_edges")

	type fileEntry struct {
		relPath string
		ext     *cachedExtraction
	}
	var files []fileEntry
	for relPath, ext := range p.extractionCache {
		if lang.ForLanguage(ext.Language) != nil {
			files = append(files, fileEntry{relPath, ext})
		}
	}

	if len(files) == 0 {
		return
	}

	results := make([][]resolvedEdge, len(files))
	numWorkers := runtime.NumCPU()
	if numWorkers > len(files) {
		numWorkers = len(files)
	}

	g := new(errgroup.Group)
	g.SetLimit(numWorkers)
	for i, fe := range files {
		g.Go(func() error {
			results[i] = p.resolveParamTypeEdges(fe.relPath, fe.ext)
			return nil
		})
	}
	_ = g.Wait()

	p.flushResolvedEdges(results)

	total := 0
	for _, r := range results {
		total += len(r)
	}
	slog.Info("pass.paramtype_edges.done", "edges", total)
}

// resolveParamTypeEdges creates USES_TYPE edges from param_types and return_type
// on function/method definitions.
func (p *Pipeline) resolveParamTypeEdges(relPath string, ext *cachedExtraction) []resolvedEdge {
	moduleQN := fqn.ModuleQN(p.ProjectName, relPath)
	importMap := p.importMaps[moduleQN]

	var edges []resolvedEdge
	seen := make(map[[2]string]bool)

	for _, def := range ext.Result.Definitions {
		if def.QualifiedName == "" {
			continue
		}

		// Process param_types
		for _, pt := range def.ParamTypes {
			typeName := cleanTypeName(pt)
			if typeName == "" || isBuiltinType(typeName) {
				continue
			}

			key := [2]string{def.QualifiedName, typeName}
			if seen[key] {
				continue
			}
			seen[key] = true

			result := p.registry.Resolve(typeName, moduleQN, importMap)
			if result.QualifiedName == "" {
				continue
			}

			edges = append(edges, resolvedEdge{
				CallerQN: def.QualifiedName,
				TargetQN: result.QualifiedName,
				Type:     "USES_TYPE",
			})
		}

		// Process return_type
		if def.ReturnType != "" {
			typeName := cleanTypeName(def.ReturnType)
			if typeName != "" && !isBuiltinType(typeName) {
				key := [2]string{def.QualifiedName, typeName}
				if !seen[key] {
					seen[key] = true
					result := p.registry.Resolve(typeName, moduleQN, importMap)
					if result.QualifiedName != "" {
						edges = append(edges, resolvedEdge{
							CallerQN: def.QualifiedName,
							TargetQN: result.QualifiedName,
							Type:     "USES_TYPE",
						})
					}
				}
			}
		}
	}

	return edges
}

// cleanTypeName strips generic parameters, array brackets, optional markers,
// and pointer symbols from a type name to get the base type.
func cleanTypeName(raw string) string {
	// Strip generic parameters: Map<string, Entity> → Map
	if idx := strings.IndexByte(raw, '<'); idx > 0 {
		raw = raw[:idx]
	}
	// Strip array brackets
	raw = strings.TrimRight(raw, "[]")
	// Strip optional/nullable markers
	raw = strings.TrimRight(raw, "?!")
	// Strip pointer/reference symbols
	raw = strings.TrimLeft(raw, "*&")
	return strings.TrimSpace(raw)
}

// isBuiltinType returns true for language primitive types that should not
// generate USES_TYPE edges.
func isBuiltinType(name string) bool {
	switch strings.ToLower(name) {
	case "string", "number", "boolean", "bool", "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "float", "float32", "float64",
		"void", "null", "undefined", "any", "unknown", "never", "object", "symbol",
		"byte", "rune", "uintptr", "complex64", "complex128", "error",
		"str", "bytes", "none", "self", "cls", "dict", "list", "tuple", "set",
		"true", "false", "promise", "array", "map", "record", "function",
		"i8", "i16", "i32", "i64", "i128", "u8", "u16", "u32", "u64", "u128",
		"f32", "f64", "usize", "isize", "char":
		return true
	}
	return false
}
