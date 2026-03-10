package cypher

import (
	"fmt"
	"strconv"
	"strings"
)

// Parser converts a token stream into an AST.
type Parser struct {
	tokens []Token
	pos    int
}

// Parse tokenizes and parses a Cypher query string into an AST.
func Parse(input string) (*Query, error) {
	tokens, err := Lex(input)
	if err != nil {
		return nil, fmt.Errorf("lex: %w", err)
	}
	p := &Parser{tokens: tokens}
	return p.parseQuery()
}

func (p *Parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokEOF}
	}
	return p.tokens[p.pos]
}

func (p *Parser) advance() Token {
	t := p.peek()
	p.pos++
	return t
}

func (p *Parser) expect(typ TokenType) error {
	t := p.advance()
	if t.Type != typ {
		return fmt.Errorf("expected %s, got %s %q at position %d", typ, t.Type, t.Value, t.Pos)
	}
	return nil
}

func (p *Parser) parseQuery() (*Query, error) {
	q := &Query{}

	// MATCH clause (required)
	if p.peek().Type != TokMatch {
		return nil, fmt.Errorf("expected MATCH at pos %d, got %q", p.peek().Pos, p.peek().Value)
	}
	m, err := p.parseMatch()
	if err != nil {
		return nil, err
	}
	q.Match = m

	// WHERE clause (optional)
	if p.peek().Type == TokWhere {
		w, err := p.parseWhere()
		if err != nil {
			return nil, err
		}
		q.Where = w
	}

	// RETURN clause (optional but common)
	if p.peek().Type == TokReturn {
		r, err := p.parseReturn()
		if err != nil {
			return nil, err
		}
		q.Return = r
	}

	return q, nil
}

func (p *Parser) parseMatch() (*MatchClause, error) {
	if err := p.expect(TokMatch); err != nil {
		return nil, err
	}
	pat, err := p.parsePattern()
	if err != nil {
		return nil, fmt.Errorf("match pattern: %w", err)
	}
	return &MatchClause{Pattern: pat}, nil
}

func (p *Parser) parsePattern() (*Pattern, error) {
	pat := &Pattern{}

	// First element must be a node
	node, err := p.parseNodePattern()
	if err != nil {
		return nil, err
	}
	pat.Elements = append(pat.Elements, node)

	// Parse alternating rel-node pairs
	for p.isRelStart() {
		rel, nextNode, err := p.parseRelAndNode()
		if err != nil {
			return nil, err
		}
		pat.Elements = append(pat.Elements, rel, nextNode)
	}

	return pat, nil
}

// isRelStart checks whether the next tokens begin a relationship pattern.
// Patterns: -[...]-> or <-[...]- or -[...]-
func (p *Parser) isRelStart() bool {
	t := p.peek()
	return t.Type == TokDash || t.Type == TokLT
}

func (p *Parser) parseRelAndNode() (*RelPattern, *NodePattern, error) {
	rel := &RelPattern{MinHops: 1, MaxHops: 1}

	// Determine direction by looking at leading token
	// Possibilities:
	//   -[...]->(node)   outbound
	//   <-[...]-(node)   inbound
	//   -[...]-(node)    any

	leadingArrow := false
	if p.peek().Type == TokLT {
		leadingArrow = true
		p.advance() // consume <
	}

	// Expect dash
	if err := p.expect(TokDash); err != nil {
		return nil, nil, fmt.Errorf("expected '-' in relationship: %w", err)
	}

	// Optional bracket section [...]
	if p.peek().Type == TokLBracket {
		if err := p.parseRelBracket(rel); err != nil {
			return nil, nil, err
		}
	}

	// Expect dash
	if err := p.expect(TokDash); err != nil {
		return nil, nil, fmt.Errorf("expected '-' after relationship: %w", err)
	}

	// Check trailing arrow
	trailingArrow := false
	if p.peek().Type == TokGT {
		trailingArrow = true
		p.advance() // consume >
	}

	// Determine direction
	switch {
	case !leadingArrow && trailingArrow:
		rel.Direction = "outbound"
	case leadingArrow && !trailingArrow:
		rel.Direction = "inbound"
	default:
		rel.Direction = "any"
	}

	// Parse the next node
	node, err := p.parseNodePattern()
	if err != nil {
		return nil, nil, err
	}

	return rel, node, nil
}

func (p *Parser) parseRelBracket(rel *RelPattern) error {
	p.advance() // consume [

	// Optional variable name
	if p.peek().Type == TokIdent {
		rel.Variable = p.advance().Value
	}

	// Optional :TYPE or :TYPE1|TYPE2
	if p.peek().Type == TokColon {
		p.advance() // consume :
		types, err := p.parseRelTypes()
		if err != nil {
			return err
		}
		rel.Types = types
	}

	// Optional *min..max for variable-length
	if p.peek().Type == TokStar {
		p.advance() // consume *
		if err := p.parseHopRange(rel); err != nil {
			return err
		}
	}

	// Expect ]
	if err := p.expect(TokRBracket); err != nil {
		return fmt.Errorf("expected ']' to close relationship: %w", err)
	}

	return nil
}

func (p *Parser) parseRelTypes() ([]string, error) {
	var types []string
	t := p.advance()
	if t.Type != TokIdent {
		return nil, fmt.Errorf("expected relationship type name, got %q at pos %d", t.Value, t.Pos)
	}
	types = append(types, t.Value)

	// Handle TYPE1|TYPE2
	for p.peek().Type == TokPipe {
		p.advance() // consume |
		t = p.advance()
		if t.Type != TokIdent {
			return nil, fmt.Errorf("expected relationship type after '|', got %q at pos %d", t.Value, t.Pos)
		}
		types = append(types, t.Value)
	}
	return types, nil
}

func (p *Parser) parseHopRange(rel *RelPattern) error {
	// Possibilities after *:
	//   *1..3   min=1, max=3
	//   *..3    min=1, max=3
	//   *1..    min=1, max=0 (unbounded)
	//   *3      min=1, max=3 (shorthand)
	//   (empty) min=1, max=0 (unbounded)

	switch p.peek().Type {
	case TokNumber:
		n, _ := strconv.Atoi(p.advance().Value)
		if p.peek().Type == TokDotDot {
			// *N..M or *N..
			rel.MinHops = n
			p.advance() // consume ..
			if p.peek().Type == TokNumber {
				m, _ := strconv.Atoi(p.advance().Value)
				rel.MaxHops = m
			} else {
				rel.MaxHops = 0 // unbounded
			}
		} else {
			// *N (shorthand for *1..N)
			rel.MinHops = 1
			rel.MaxHops = n
		}
	case TokDotDot:
		// *..M
		p.advance() // consume ..
		rel.MinHops = 1
		if p.peek().Type == TokNumber {
			m, _ := strconv.Atoi(p.advance().Value)
			rel.MaxHops = m
		} else {
			rel.MaxHops = 0
		}
	default:
		// Just * with no range: unbounded
		rel.MinHops = 1
		rel.MaxHops = 0
	}

	return nil
}

func (p *Parser) parseNodePattern() (*NodePattern, error) {
	if err := p.expect(TokLParen); err != nil {
		return nil, fmt.Errorf("expected '(' for node pattern: %w", err)
	}

	node := &NodePattern{}

	// Optional variable name
	if p.peek().Type == TokIdent {
		node.Variable = p.advance().Value
	}

	// Optional :Label
	if p.peek().Type == TokColon {
		p.advance() // consume :
		t := p.advance()
		if t.Type != TokIdent {
			return nil, fmt.Errorf("expected label name after ':', got %q at pos %d", t.Value, t.Pos)
		}
		node.Label = t.Value
	}

	// Optional {key: "val", ...}
	if p.peek().Type == TokLBrace {
		props, err := p.parseInlineProps()
		if err != nil {
			return nil, err
		}
		node.Props = props
	}

	if err := p.expect(TokRParen); err != nil {
		return nil, fmt.Errorf("expected ')' to close node pattern: %w", err)
	}

	return node, nil
}

func (p *Parser) parseInlineProps() (map[string]string, error) {
	p.advance() // consume {
	props := make(map[string]string)

	for p.peek().Type != TokRBrace {
		if len(props) > 0 {
			if err := p.expect(TokComma); err != nil {
				return nil, fmt.Errorf("expected ',' between properties: %w", err)
			}
		}

		// key
		keyTok := p.advance()
		if keyTok.Type != TokIdent {
			return nil, fmt.Errorf("expected property key, got %q at pos %d", keyTok.Value, keyTok.Pos)
		}

		// :
		if err := p.expect(TokColon); err != nil {
			return nil, fmt.Errorf("expected ':' after property key: %w", err)
		}

		// value (string)
		valTok := p.advance()
		if valTok.Type != TokString {
			return nil, fmt.Errorf("expected string value for property %q, got %q at pos %d", keyTok.Value, valTok.Value, valTok.Pos)
		}

		props[keyTok.Value] = valTok.Value
	}

	p.advance() // consume }
	return props, nil
}

func (p *Parser) parseWhere() (*WhereClause, error) {
	p.advance() // consume WHERE

	root, err := p.parseOrExpr()
	if err != nil {
		return nil, err
	}

	w := &WhereClause{Root: &root}

	// Populate legacy flat fields for backward compatibility.
	// If root is a single AND or OR group with only conditions (no sub-groups),
	// flatten into the legacy Conditions/Operator fields.
	w.Conditions = flattenConditions(&root)
	w.Operator = root.Operator

	return w, nil
}

// parseOrExpr parses OR-separated AND-groups (OR has lower precedence).
func (p *Parser) parseOrExpr() (ConditionGroup, error) {
	first, err := p.parseAndExpr()
	if err != nil {
		return ConditionGroup{}, err
	}

	// If no OR follows, return the AND group directly
	if p.peek().Type != TokOr {
		return first, nil
	}

	// Collect AND-groups joined by OR
	orGroup := ConditionGroup{Operator: "OR"}
	orGroup.Groups = append(orGroup.Groups, first)

	for p.peek().Type == TokOr {
		p.advance() // consume OR
		next, err := p.parseAndExpr()
		if err != nil {
			return ConditionGroup{}, err
		}
		orGroup.Groups = append(orGroup.Groups, next)
	}

	return orGroup, nil
}

// parseAndExpr parses AND-separated conditions (AND has higher precedence).
func (p *Parser) parseAndExpr() (ConditionGroup, error) {
	cond, err := p.parseCondition()
	if err != nil {
		return ConditionGroup{}, err
	}

	group := ConditionGroup{Operator: "AND"}
	group.Conditions = append(group.Conditions, cond)

	for p.peek().Type == TokAnd {
		p.advance() // consume AND
		cond, err := p.parseCondition()
		if err != nil {
			return ConditionGroup{}, err
		}
		group.Conditions = append(group.Conditions, cond)
	}

	return group, nil
}

// flattenConditions returns all conditions from a group tree as a flat slice.
func flattenConditions(g *ConditionGroup) []Condition {
	var result []Condition
	result = append(result, g.Conditions...)
	for i := range g.Groups {
		result = append(result, flattenConditions(&g.Groups[i])...)
	}
	return result
}

func (p *Parser) parseCondition() (Condition, error) {
	c := Condition{}

	// Optional NOT prefix
	if p.peek().Type == TokNot {
		p.advance()
		c.Negated = true
	}

	// Parse LHS expression
	lhs, err := p.parseExpr()
	if err != nil {
		return c, fmt.Errorf("condition LHS: %w", err)
	}
	c.LHS = lhs

	// Operator
	op := p.peek()
	switch op.Type {
	case TokEQ:
		c.Operator = "="
		p.advance()
	case TokRegex:
		c.Operator = "=~"
		p.advance()
	case TokGT:
		c.Operator = ">"
		p.advance()
	case TokLT:
		c.Operator = "<"
		p.advance()
	case TokGTE:
		c.Operator = ">="
		p.advance()
	case TokLTE:
		c.Operator = "<="
		p.advance()
	case TokContains:
		c.Operator = "CONTAINS"
		p.advance()
	case TokStarts:
		// STARTS WITH
		p.advance() // consume STARTS
		if p.peek().Type != TokWith {
			return c, fmt.Errorf("expected WITH after STARTS at pos %d", p.peek().Pos)
		}
		p.advance() // consume WITH
		c.Operator = "STARTS WITH"
	case TokEnds:
		// ENDS WITH
		p.advance() // consume ENDS
		if p.peek().Type != TokWith {
			return c, fmt.Errorf("expected WITH after ENDS at pos %d", p.peek().Pos)
		}
		p.advance() // consume WITH
		c.Operator = "ENDS WITH"
	case TokIn:
		// IN [list]
		p.advance() // consume IN
		c.Operator = "IN"

		// Parse the list expression as RHS
		listExpr, listErr := p.parseListLiteral()
		if listErr != nil {
			return c, fmt.Errorf("IN list: %w", listErr)
		}
		c.RHS = listExpr

		// Flatten to legacy fields when possible: simple PropertyExpr LHS
		if prop, ok := lhs.(*PropertyExpr); ok {
			c.Variable = prop.Variable
			c.Property = prop.Property
		}

		return c, nil
	default:
		return c, fmt.Errorf("expected comparison operator, got %q at pos %d", op.Value, op.Pos)
	}

	// Parse RHS expression
	rhs, err := p.parseExpr()
	if err != nil {
		return c, fmt.Errorf("condition RHS: %w", err)
	}
	c.RHS = rhs

	// Flatten to legacy fields when possible: simple PropertyExpr LHS + LiteralExpr RHS
	if prop, ok := lhs.(*PropertyExpr); ok {
		c.Variable = prop.Variable
		c.Property = prop.Property
	}
	if lit, ok := rhs.(*LiteralExpr); ok {
		c.Value = lit.Value
	}

	return c, nil
}

// parsePrimaryExpr parses a primary expression: number, string, list literal, or variable.property.
func (p *Parser) parsePrimaryExpr() (Expr, error) {
	tok := p.peek()
	switch tok.Type {
	case TokNumber:
		p.advance()
		return &LiteralExpr{Value: tok.Value}, nil
	case TokString:
		p.advance()
		return &LiteralExpr{Value: tok.Value}, nil
	case TokLBracket:
		return p.parseListLiteral()
	case TokIdent:
		p.advance()
		// Boolean literals: true/false (case-insensitive)
		lower := strings.ToLower(tok.Value)
		if lower == "true" || lower == "false" || lower == "null" {
			return &LiteralExpr{Value: lower}, nil
		}
		if p.peek().Type == TokDot {
			p.advance() // consume '.'
			propTok := p.advance()
			if propTok.Type != TokIdent {
				return nil, fmt.Errorf("expected property name after '.', got %s %q at position %d", propTok.Type, propTok.Value, propTok.Pos)
			}
			return &PropertyExpr{Variable: tok.Value, Property: propTok.Value}, nil
		}
		return nil, fmt.Errorf("expected '.' after variable %q at position %d", tok.Value, tok.Pos)
	default:
		return nil, fmt.Errorf("expected string, number, or variable.property in expression, got %s %q at position %d", tok.Type, tok.Value, tok.Pos)
	}
}

// parseListLiteral parses [expr, expr, ...] into a ListExpr.
func (p *Parser) parseListLiteral() (*ListExpr, error) {
	if err := p.expect(TokLBracket); err != nil {
		return nil, fmt.Errorf("expected '[' for list: %w", err)
	}

	list := &ListExpr{}
	if p.peek().Type == TokRBracket {
		p.advance() // empty list
		return list, nil
	}

	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	list.Values = append(list.Values, first)

	for p.peek().Type == TokComma {
		p.advance() // consume comma
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		list.Values = append(list.Values, val)
	}

	if err := p.expect(TokRBracket); err != nil {
		return nil, fmt.Errorf("expected ']' to close list: %w", err)
	}

	return list, nil
}

// parseExpr parses an expression with optional arithmetic operators (+, -, *).
func (p *Parser) parseExpr() (Expr, error) {
	left, err := p.parsePrimaryExpr()
	if err != nil {
		return nil, err
	}

	for p.peek().Type == TokPlus || p.peek().Type == TokDash || p.peek().Type == TokStar {
		opTok := p.advance()
		right, err := p.parsePrimaryExpr()
		if err != nil {
			return nil, err
		}
		left = &ArithExpr{Left: left, Op: opTok.Value, Right: right}
	}

	return left, nil
}

func (p *Parser) parseReturn() (*ReturnClause, error) {
	p.advance() // consume RETURN
	r := &ReturnClause{OrderDir: "ASC"}

	// Optional DISTINCT
	if p.peek().Type == TokDistinct {
		r.Distinct = true
		p.advance()
	}

	// Parse return items
	item, err := p.parseReturnItem()
	if err != nil {
		return nil, err
	}
	r.Items = append(r.Items, item)

	for p.peek().Type == TokComma {
		p.advance() // consume ,
		item, err := p.parseReturnItem()
		if err != nil {
			return nil, err
		}
		r.Items = append(r.Items, item)
	}

	// Optional ORDER BY
	if p.peek().Type == TokOrder {
		orderBy, orderDir, err := p.parseOrderBy()
		if err != nil {
			return nil, err
		}
		r.OrderBy = orderBy
		r.OrderDir = orderDir
	}

	// Optional LIMIT
	if p.peek().Type == TokLimit {
		p.advance() // consume LIMIT
		numTok := p.advance()
		if numTok.Type != TokNumber {
			return nil, fmt.Errorf("expected number after LIMIT, got %q", numTok.Value)
		}
		n, _ := strconv.Atoi(numTok.Value)
		r.Limit = n
	}

	return r, nil
}

func (p *Parser) parseReturnItem() (ReturnItem, error) {
	item := ReturnItem{}

	// Check for COUNT(variable)
	if p.peek().Type == TokCount {
		return p.parseCountItem()
	}

	// Try parsing as a full expression (supports arithmetic like f.end_line - f.start_line)
	expr, err := p.parseReturnExpr()
	if err != nil {
		return item, err
	}

	// If it's a simple property access, keep backward-compatible fields
	switch ex := expr.(type) {
	case *PropertyExpr:
		item.Variable = ex.Variable
		item.Property = ex.Property
	case *VariableExpr:
		item.Variable = ex.Variable
	default:
		// Complex expression (arithmetic etc.) — store as Expr
		item.Expr = expr
	}

	// Optional AS alias
	if p.peek().Type == TokAs {
		p.advance() // consume AS
		aliasTok := p.advance()
		if aliasTok.Type != TokIdent {
			return item, fmt.Errorf("expected alias after AS, got %q", aliasTok.Value)
		}
		item.Alias = aliasTok.Value
	}

	return item, nil
}

// parseReturnExpr parses an expression in RETURN context, allowing bare variables.
func (p *Parser) parseReturnExpr() (Expr, error) {
	left, err := p.parseReturnPrimaryExpr()
	if err != nil {
		return nil, err
	}
	for p.peek().Type == TokPlus || p.peek().Type == TokDash || p.peek().Type == TokStar {
		opTok := p.advance()
		right, err := p.parseReturnPrimaryExpr()
		if err != nil {
			return nil, err
		}
		left = &ArithExpr{Left: left, Op: opTok.Value, Right: right}
	}
	return left, nil
}

// parseReturnPrimaryExpr is like parsePrimaryExpr but allows bare variables (for RETURN f).
func (p *Parser) parseReturnPrimaryExpr() (Expr, error) {
	tok := p.peek()
	switch tok.Type {
	case TokNumber:
		p.advance()
		return &LiteralExpr{Value: tok.Value}, nil
	case TokString:
		p.advance()
		return &LiteralExpr{Value: tok.Value}, nil
	case TokIdent:
		p.advance()
		if p.peek().Type == TokDot {
			p.advance() // consume '.'
			propTok := p.advance()
			if propTok.Type != TokIdent {
				return nil, fmt.Errorf("expected property name after '.', got %s %q at position %d", propTok.Type, propTok.Value, propTok.Pos)
			}
			return &PropertyExpr{Variable: tok.Value, Property: propTok.Value}, nil
		}
		// Bare variable (e.g. RETURN f)
		return &VariableExpr{Variable: tok.Value}, nil
	default:
		return nil, fmt.Errorf("expected variable or expression in RETURN item, got %s %q at position %d", tok.Type, tok.Value, tok.Pos)
	}
}

// parseCountItem parses a COUNT(variable) [AS alias] expression.
func (p *Parser) parseCountItem() (ReturnItem, error) {
	item := ReturnItem{}
	p.advance() // consume COUNT
	item.Func = "COUNT"
	if err := p.expect(TokLParen); err != nil {
		return item, fmt.Errorf("expected '(' after COUNT: %w", err)
	}
	varTok := p.advance()
	if varTok.Type != TokIdent {
		return item, fmt.Errorf("expected variable in COUNT(), got %q", varTok.Value)
	}
	item.Variable = varTok.Value
	if err := p.expect(TokRParen); err != nil {
		return item, fmt.Errorf("expected ')' after COUNT variable: %w", err)
	}

	// Optional AS alias
	if p.peek().Type == TokAs {
		p.advance() // consume AS
		aliasTok := p.advance()
		if aliasTok.Type != TokIdent {
			return item, fmt.Errorf("expected alias after AS, got %q", aliasTok.Value)
		}
		item.Alias = aliasTok.Value
	}

	return item, nil
}

// parseOrderBy parses ORDER BY <field|COUNT(var)> [ASC|DESC].
func (p *Parser) parseOrderBy() (orderBy, orderDir string, err error) {
	p.advance() // consume ORDER
	if err := p.expect(TokBy); err != nil {
		return "", "", fmt.Errorf("expected BY after ORDER: %w", err)
	}

	orderField, err := p.parseOrderField()
	if err != nil {
		return "", "", err
	}

	// Optional ASC/DESC
	dir := ""
	if p.peek().Type == TokAsc {
		dir = "ASC"
		p.advance()
	} else if p.peek().Type == TokDesc {
		dir = "DESC"
		p.advance()
	}
	return orderField, dir, nil
}

// parseOrderField parses the field expression in ORDER BY: either COUNT(var) or var[.prop].
func (p *Parser) parseOrderField() (string, error) {
	if p.peek().Type == TokCount {
		p.advance() // consume COUNT
		if err := p.expect(TokLParen); err != nil {
			return "", fmt.Errorf("expected '(' after COUNT in ORDER BY: %w", err)
		}
		varTok := p.advance()
		if varTok.Type != TokIdent {
			return "", fmt.Errorf("expected variable in COUNT(), got %q", varTok.Value)
		}
		if err := p.expect(TokRParen); err != nil {
			return "", fmt.Errorf("expected ')' after COUNT variable in ORDER BY: %w", err)
		}
		return "COUNT(" + varTok.Value + ")", nil
	}

	orderTok := p.advance()
	if orderTok.Type != TokIdent {
		return "", fmt.Errorf("expected field name for ORDER BY, got %q", orderTok.Value)
	}
	field := orderTok.Value
	if p.peek().Type == TokDot {
		p.advance() // consume .
		propTok := p.advance()
		field += "." + propTok.Value
	}
	return field, nil
}
