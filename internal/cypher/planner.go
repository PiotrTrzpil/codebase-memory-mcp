package cypher

import "fmt"

// Plan represents an execution plan for a parsed Cypher query.
type Plan struct {
	Steps      []PlanStep
	ReturnSpec *ReturnClause
}

// PlanStep is a single step in the execution plan.
type PlanStep interface {
	stepType() string
}

// ScanNodes finds nodes matching label and/or inline property filters.
type ScanNodes struct {
	Variable string
	Label    string
	Props    map[string]string // inline property filters
}

func (*ScanNodes) stepType() string { return "scan" }

// ExpandRelationship follows edges from bound nodes to match target nodes.
type ExpandRelationship struct {
	FromVar   string // source variable (already bound)
	ToVar     string // target variable (to bind)
	RelVar    string // optional relationship variable (to bind edge)
	ToLabel   string // optional label filter on target
	ToProps   map[string]string
	EdgeTypes []string // required edge types
	Direction string   // "outbound", "inbound", "any"
	MinHops   int
	MaxHops   int
}

func (*ExpandRelationship) stepType() string { return "expand" }

// FilterWhere applies WHERE conditions to the bindings.
type FilterWhere struct {
	Conditions []Condition
	Operator   string // "AND" or "OR"
	Root       *ConditionGroup
}

func (*FilterWhere) stepType() string { return "filter" }

// UnwindStep expands JSON array properties into individual rows.
type UnwindStep struct {
	Expression Expr
	Alias      string
}

func (*UnwindStep) stepType() string { return "unwind" }

// asNodePattern safely type-asserts a PatternElement to *NodePattern.
func asNodePattern(el PatternElement) (*NodePattern, error) {
	np, ok := el.(*NodePattern)
	if !ok {
		return nil, fmt.Errorf("expected *NodePattern, got %T", el)
	}
	return np, nil
}

// asRelPattern safely type-asserts a PatternElement to *RelPattern.
func asRelPattern(el PatternElement) (*RelPattern, error) {
	rp, ok := el.(*RelPattern)
	if !ok {
		return nil, fmt.Errorf("expected *RelPattern, got %T", el)
	}
	return rp, nil
}

// BuildPlan converts a parsed Query AST into an execution Plan.
func BuildPlan(q *Query) (*Plan, error) {
	plan := &Plan{ReturnSpec: q.Return}

	elements := q.Match.Pattern.Elements
	if len(elements) == 0 {
		return plan, nil
	}

	// First element is always a node pattern
	firstNode, err := asNodePattern(elements[0])
	if err != nil {
		return nil, fmt.Errorf("first pattern element: %w", err)
	}
	plan.Steps = append(plan.Steps, &ScanNodes{
		Variable: firstNode.Variable,
		Label:    firstNode.Label,
		Props:    firstNode.Props,
	})

	// Optimization: push WHERE conditions that reference only the first
	// scan variable BEFORE any expand steps. This dramatically reduces the
	// number of bindings that need to be expanded.
	earlyFilters, lateWhere := splitWhereFilters(q.Where, firstNode.Variable, len(elements) > 1)

	// Insert early filter right after scan
	if len(earlyFilters) > 0 {
		plan.Steps = append(plan.Steps, &FilterWhere{
			Conditions: earlyFilters,
			Operator:   "AND",
		})
	}

	// Process relationship-node pairs
	for i := 1; i+1 < len(elements); i += 2 {
		rel, err := asRelPattern(elements[i])
		if err != nil {
			return nil, fmt.Errorf("element[%d]: %w", i, err)
		}
		targetNode, err := asNodePattern(elements[i+1])
		if err != nil {
			return nil, fmt.Errorf("element[%d]: %w", i+1, err)
		}
		fromNode, err := asNodePattern(elements[i-1])
		if err != nil {
			return nil, fmt.Errorf("element[%d]: %w", i-1, err)
		}

		plan.Steps = append(plan.Steps, &ExpandRelationship{
			FromVar:   fromNode.Variable,
			ToVar:     targetNode.Variable,
			RelVar:    rel.Variable,
			ToLabel:   targetNode.Label,
			ToProps:   targetNode.Props,
			EdgeTypes: rel.Types,
			Direction: rel.Direction,
			MinHops:   rel.MinHops,
			MaxHops:   rel.MaxHops,
		})
	}

	// Late WHERE filter (conditions referencing expand variables)
	if lateWhere != nil {
		plan.Steps = append(plan.Steps, &FilterWhere{
			Conditions: lateWhere.Conditions,
			Operator:   lateWhere.Operator,
			Root:       lateWhere.Root,
		})
	} else if q.Where != nil && len(earlyFilters) == 0 {
		// No split happened — add all conditions at the end
		plan.Steps = append(plan.Steps, &FilterWhere{
			Conditions: q.Where.Conditions,
			Operator:   q.Where.Operator,
			Root:       q.Where.Root,
		})
	}

	// UNWIND clause (JSON array expansion)
	if q.Unwind != nil {
		plan.Steps = append(plan.Steps, &UnwindStep{
			Expression: q.Unwind.Expression,
			Alias:      q.Unwind.Alias,
		})
	}

	return plan, nil
}

// exprVariables collects all variable names referenced in an expression.
func exprVariables(e Expr) []string {
	if e == nil {
		return nil
	}
	switch ex := e.(type) {
	case *PropertyExpr:
		return []string{ex.Variable}
	case *LiteralExpr:
		return nil
	case *ArithExpr:
		return append(exprVariables(ex.Left), exprVariables(ex.Right)...)
	case *ListExpr:
		var vars []string
		for _, v := range ex.Values {
			vars = append(vars, exprVariables(v)...)
		}
		return vars
	default:
		return nil
	}
}

// conditionVariables returns all variables referenced by a condition.
func conditionVariables(c Condition) []string {
	if c.LHS != nil {
		vars := exprVariables(c.LHS)
		vars = append(vars, exprVariables(c.RHS)...)
		return vars
	}
	return []string{c.Variable}
}

// splitWhereFilters separates WHERE conditions into early (scan-only) and late filters.
// Returns the early conditions as a flat slice and the remaining WHERE clause (or nil).
func splitWhereFilters(where *WhereClause, scanVar string, hasExpand bool) (early []Condition, lateWhere *WhereClause) {
	if where == nil {
		return nil, nil
	}

	// When the root group is AND at the top level, we can extract scan-only conditions.
	if hasExpand && where.Root != nil && where.Root.Operator == "AND" && len(where.Root.Groups) == 0 {
		// Simple AND group with only conditions — same as old behavior
		var late []Condition
		for _, c := range where.Root.Conditions {
			vars := conditionVariables(c)
			allScan := true
			for _, v := range vars {
				if v != scanVar {
					allScan = false
					break
				}
			}
			if allScan {
				early = append(early, c)
			} else {
				late = append(late, c)
			}
		}
		if len(late) > 0 {
			return early, &WhereClause{
				Conditions: late,
				Operator:   "AND",
				Root:       &ConditionGroup{Conditions: late, Operator: "AND"},
			}
		}
		return early, nil
	}

	// For mixed AND/OR or legacy: can't split, return everything as late
	if hasExpand && where.Operator == "AND" && where.Root == nil {
		// Legacy path (no Root)
		var late []Condition
		for _, c := range where.Conditions {
			vars := conditionVariables(c)
			allScan := true
			for _, v := range vars {
				if v != scanVar {
					allScan = false
					break
				}
			}
			if allScan {
				early = append(early, c)
			} else {
				late = append(late, c)
			}
		}
		if len(late) > 0 {
			return early, &WhereClause{Conditions: late, Operator: "AND"}
		}
		return early, nil
	}

	// Can't split OR or mixed conditions
	return nil, where
}
