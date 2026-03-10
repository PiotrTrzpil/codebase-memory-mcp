package tools

import (
	"context"
	"fmt"

	"github.com/DeusData/codebase-memory-mcp/internal/cypher"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) handleQueryGraph(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args, err := parseArgs(req)
	if err != nil {
		return errResult(err.Error()), nil
	}

	query := getStringArg(args, "query")
	if query == "" {
		return errResult("missing required 'query' parameter"), nil
	}

	st, err := s.resolveStore(getStringArg(args, "project"))
	if err != nil {
		return errResult(fmt.Sprintf("resolve store: %v", err)), nil
	}

	exec := &cypher.Executor{Store: st}
	result, err := exec.Execute(query)
	if err != nil {
		return errResult(fmt.Sprintf("query error: %v\n\nSupported Cypher subset:\n"+
			"  MATCH (var:Label)-[:TYPE]->(var2) WHERE ... RETURN ...\n"+
			"  Operators: =, =~, >, <, >=, <=, CONTAINS, STARTS WITH, NOT\n"+
			"  Arithmetic: var.prop - var.prop > value\n"+
			"  Property comparison: a.prop < b.prop\n"+
			"  Aggregation: COUNT(var), DISTINCT\n"+
			"  Modifiers: ORDER BY field ASC|DESC, LIMIT n\n"+
			"  Variable-length: -[:TYPE*1..3]->\n"+
			"  Multi-type: -[:CALLS|HTTP_CALLS]->", err)), nil
	}

	responseData := map[string]any{
		"columns": result.Columns,
		"rows":    result.Rows,
		"total":   len(result.Rows),
	}
	s.addIndexStatus(responseData)

	res := jsonResult(responseData)
	s.addUpdateNotice(res)
	return res, nil
}
