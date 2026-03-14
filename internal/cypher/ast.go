package cypher

// Query represents a parsed Cypher query.
type Query struct {
	Match  *MatchClause
	Where  *WhereClause
	Unwind *UnwindClause
	Return *ReturnClause
}

// MatchClause holds the MATCH pattern.
type MatchClause struct {
	Pattern *Pattern
}

// Pattern is a sequence of alternating nodes and relationships.
type Pattern struct {
	Elements []PatternElement
}

// PatternElement is either a NodePattern or a RelPattern.
type PatternElement interface {
	patternElement()
}

// NodePattern matches a graph node with optional label and inline properties.
type NodePattern struct {
	Variable string            // e.g. "f"
	Label    string            // e.g. "Function" (optional)
	Props    map[string]string // inline property filters (optional)
}

func (*NodePattern) patternElement() {}

// RelPattern matches a graph relationship with optional types, direction, and hops.
type RelPattern struct {
	Variable  string   // (optional)
	Types     []string // relationship types, e.g. ["CALLS", "HTTP_CALLS"]
	Direction string   // "outbound", "inbound", "any"
	MinHops   int      // for variable-length, default 1
	MaxHops   int      // for variable-length, default 1 (0 means unbounded)
}

func (*RelPattern) patternElement() {}

// WhereClause holds filter conditions joined by AND/OR.
// When Root is non-nil, it is used instead of the flat Conditions/Operator fields.
type WhereClause struct {
	Conditions []Condition // legacy flat list (used when Root is nil)
	Operator   string      // "AND" or "OR" (used when Root is nil)
	Root       *ConditionGroup
}

// ConditionGroup represents a group of conditions joined by a single operator.
// Groups can be nested to support mixed AND/OR: (a AND b) OR c
type ConditionGroup struct {
	Conditions []Condition
	Groups     []ConditionGroup
	Operator   string // "AND" or "OR"
}

// UnwindClause unwinds a JSON array property into individual rows.
// Syntax: UNWIND r.first_arg AS event
type UnwindClause struct {
	Expression Expr   // the expression to unwind (e.g., PropertyExpr for r.first_arg)
	Alias      string // variable name for each element
}

// Condition is a single property comparison.
type Condition struct {
	Variable string // "f"
	Property string // "name"
	Operator string // "=", "=~", "CONTAINS", "STARTS WITH", ">", "<", ">=", "<="
	Value    string // the comparison value
	Negated  bool   // true when prefixed with NOT
	LHS      Expr   // expression-based left-hand side (nil for legacy simple conditions)
	RHS      Expr   // expression-based right-hand side (nil for legacy simple conditions)
}

// Expr represents an expression in a WHERE condition (property access, literal, or arithmetic).
type Expr interface{ exprNode() }

// PropertyExpr is a variable.property access (e.g. f.start_line).
type PropertyExpr struct {
	Variable string
	Property string
}

func (*PropertyExpr) exprNode() {}

// VariableExpr is a bare variable reference (e.g. f in RETURN f).
type VariableExpr struct{ Variable string }

func (*VariableExpr) exprNode() {}

// LiteralExpr is a string or numeric literal.
type LiteralExpr struct{ Value string }

func (*LiteralExpr) exprNode() {}

// ArithExpr is a binary arithmetic expression (e.g. m.end_line - m.start_line).
type ArithExpr struct {
	Left  Expr
	Op    string // "+", "-", "*"
	Right Expr
}

func (*ArithExpr) exprNode() {}

// ListExpr is a list literal [expr, expr, ...] used with the IN operator.
type ListExpr struct {
	Values []Expr
}

func (*ListExpr) exprNode() {}

// ReturnClause specifies which data to return from the query.
type ReturnClause struct {
	Items    []ReturnItem
	OrderBy  string // "f.name" (optional)
	OrderDir string // "ASC" or "DESC"
	Limit    int    // 0 means no limit
	Distinct bool
}

// ReturnItem is a single item in the RETURN clause.
type ReturnItem struct {
	Variable string // "f"
	Property string // "name" (empty = return whole node)
	Alias    string // "AS call_count" (optional)
	Func     string // "COUNT" (optional aggregation)
	Expr     Expr   // expression (non-nil for arithmetic like f.end_line - f.start_line)
}
