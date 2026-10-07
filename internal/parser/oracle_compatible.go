package parser

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cuipengdba/agentsql/internal/model"
)

// oracleCompatibleParser implements the frozen v0.5 DM8/Oracle/SQL Server lineage
// profile. It is deliberately independent from the execution validator: adding
// parser support must not change which statements an executor accepts.
type oracleCompatibleParser struct {
	dialect model.DBDialect
}

var _ Parser = (*oracleCompatibleParser)(nil)

const (
	oracleCompatibleMaxSQLBytes = 1 << 20
	oracleCompatibleMaxTokens   = 32768
)

type oracleTokenKind uint8

const (
	oracleTokenEOF oracleTokenKind = iota
	oracleTokenWord
	oracleTokenQuotedIdentifier
	oracleTokenString
	oracleTokenNumber
	oracleTokenComma
	oracleTokenDot
	oracleTokenStar
	oracleTokenOperator
)

type oracleToken struct {
	kind oracleTokenKind
	text string
}

type oracleIdentifier struct {
	name   string
	quoted bool
}

type oracleColumnRef struct {
	parts []oracleIdentifier
}

type oracleProjection struct {
	column        *oracleColumnRef
	starQualifier []oracleIdentifier
	star          bool
	constant      bool
	alias         string
	aliasQuoted   bool
}

type oracleRelation struct {
	object    model.ObjectRef
	name      oracleIdentifier
	alias     oracleIdentifier
	hasAlias  bool
	schema    oracleIdentifier
	hasSchema bool
}

type oracleTruth uint8

const (
	oracleTruthUnknown oracleTruth = iota
	oracleTruthFalse
	oracleTruthTrue
)

type oracleOperand struct {
	column      *oracleColumnRef
	literalKind oracleTokenKind
	literal     string
}

type oracleSelectParser struct {
	dialect       model.DBDialect
	tokens        []oracleToken
	position      int
	projections   []oracleProjection
	relations     []oracleRelation
	columns       stringSet
	references    []oracleColumnRef
	hasPredicate  bool
	whereTruth    oracleTruth
	hasPagination bool
	hasTop        bool
	allowRownum   bool
	hasRownum     bool
}

func (parser *oracleCompatibleParser) Parse(sql string) (ast *model.AST, err error) {
	dialect := parser.dialect
	defer func() {
		if recovered := recover(); recovered != nil {
			ast = &model.AST{Dialect: dialect, RawSQL: sql}
			err = recoveredError(dialect, recovered)
		}
	}()
	if dialect != dmDialect && dialect != oracleDialect && dialect != sqlserverDialect {
		return &model.AST{Dialect: dialect, RawSQL: sql}, unparseableError(
			dialect, errors.New("unsupported Oracle-compatible parser dialect"),
		)
	}
	if strings.TrimSpace(sql) == "" {
		return &model.AST{Dialect: dialect, RawSQL: sql}, unparseableError(dialect, errors.New("SQL is empty"))
	}
	if len(sql) > oracleCompatibleMaxSQLBytes || !utf8.ValidString(sql) {
		return &model.AST{Dialect: dialect, RawSQL: sql}, unparseableError(
			dialect, errors.New("SQL exceeds the size limit or is not valid UTF-8"),
		)
	}
	tokens, tokenizeErr := tokenizeOracleCompatible(sql, dialect)
	if tokenizeErr != nil {
		return &model.AST{Dialect: dialect, RawSQL: sql}, unparseableError(dialect, tokenizeErr)
	}
	builder := &oracleSelectParser{dialect: dialect, tokens: tokens, columns: make(stringSet)}
	if parseErr := builder.parseSelect(); parseErr != nil {
		return &model.AST{Dialect: dialect, RawSQL: sql}, unparseableError(dialect, parseErr)
	}
	result, buildErr := builder.buildAST(sql)
	if buildErr != nil {
		return &model.AST{Dialect: dialect, RawSQL: sql}, unparseableError(dialect, buildErr)
	}
	return result, nil
}

func tokenizeOracleCompatible(sql string, dialect model.DBDialect) ([]oracleToken, error) {
	tokens := make([]oracleToken, 0, min(len(sql)/2, 256))
	appendToken := func(kind oracleTokenKind, text string) error {
		if len(tokens) >= oracleCompatibleMaxTokens {
			return fmt.Errorf("token count exceeds limit %d", oracleCompatibleMaxTokens)
		}
		tokens = append(tokens, oracleToken{kind: kind, text: text})
		return nil
	}
	for position := 0; position < len(sql); {
		current, size := utf8.DecodeRuneInString(sql[position:])
		switch {
		case unicode.IsSpace(current):
			position += size
		case unicode.IsControl(current):
			return nil, errors.New("control character is unsupported")
		case current == ';':
			return nil, errors.New("statement separators are unsupported")
		case current == '?' || current == ':' || current == '$':
			return nil, errors.New("bind markers are unsupported")
		case current == '(' || current == ')':
			return nil, errors.New("parenthesized expressions, functions, and subqueries are unsupported")
		case current == '`' || (current == ']' && dialect != sqlserverDialect) || (current == '[' && dialect != sqlserverDialect):
			return nil, errors.New("non-Oracle identifier quoting is unsupported")
		case current == '[':
			position++
			var value strings.Builder
			closed := false
			for position < len(sql) {
				if sql[position] != ']' {
					runeValue, runeSize := utf8.DecodeRuneInString(sql[position:])
					value.WriteRune(runeValue)
					position += runeSize
					continue
				}
				if position+1 < len(sql) && sql[position+1] == ']' {
					value.WriteByte(']')
					position += 2
					continue
				}
				position++
				closed = true
				break
			}
			if !closed || value.Len() == 0 {
				return nil, errors.New("empty or unterminated bracket identifier")
			}
			if err := appendToken(oracleTokenQuotedIdentifier, value.String()); err != nil {
				return nil, err
			}
		case current == '-' && position+1 < len(sql) && sql[position+1] == '-':
			return nil, errors.New("comments are unsupported")
		case current == '/' && position+1 < len(sql) && sql[position+1] == '*':
			return nil, errors.New("comments are unsupported")
		case current == '\'':
			start := position
			position++
			closed := false
			for position < len(sql) {
				if sql[position] != '\'' {
					_, runeSize := utf8.DecodeRuneInString(sql[position:])
					position += runeSize
					continue
				}
				if position+1 < len(sql) && sql[position+1] == '\'' {
					position += 2
					continue
				}
				position++
				closed = true
				break
			}
			if !closed {
				return nil, errors.New("unterminated string literal")
			}
			if err := appendToken(oracleTokenString, sql[start:position]); err != nil {
				return nil, err
			}
		case current == '"':
			position++
			var value strings.Builder
			closed := false
			for position < len(sql) {
				if sql[position] != '"' {
					runeValue, runeSize := utf8.DecodeRuneInString(sql[position:])
					value.WriteRune(runeValue)
					position += runeSize
					continue
				}
				if position+1 < len(sql) && sql[position+1] == '"' {
					value.WriteByte('"')
					position += 2
					continue
				}
				position++
				closed = true
				break
			}
			if !closed || value.Len() == 0 {
				return nil, errors.New("empty or unterminated quoted identifier")
			}
			if err := appendToken(oracleTokenQuotedIdentifier, value.String()); err != nil {
				return nil, err
			}
		case unicode.IsLetter(current) || current == '_':
			start := position
			position += size
			for position < len(sql) {
				next, nextSize := utf8.DecodeRuneInString(sql[position:])
				if !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_' && next != '$' && next != '#' {
					break
				}
				position += nextSize
			}
			if err := appendToken(oracleTokenWord, sql[start:position]); err != nil {
				return nil, err
			}
		case unicode.IsDigit(current):
			start := position
			position += size
			dotted := false
			for position < len(sql) {
				next, nextSize := utf8.DecodeRuneInString(sql[position:])
				if unicode.IsDigit(next) {
					position += nextSize
					continue
				}
				if next == '.' && !dotted && position+1 < len(sql) {
					after, _ := utf8.DecodeRuneInString(sql[position+1:])
					if unicode.IsDigit(after) {
						dotted = true
						position++
						continue
					}
				}
				break
			}
			if err := appendToken(oracleTokenNumber, sql[start:position]); err != nil {
				return nil, err
			}
		case current == ',':
			if err := appendToken(oracleTokenComma, ","); err != nil {
				return nil, err
			}
			position++
		case current == '.':
			if err := appendToken(oracleTokenDot, "."); err != nil {
				return nil, err
			}
			position++
		case current == '*':
			if err := appendToken(oracleTokenStar, "*"); err != nil {
				return nil, err
			}
			position++
		case strings.ContainsRune("=<>!", current):
			start := position
			position += size
			if position < len(sql) && (sql[position] == '=' || (current == '<' && sql[position] == '>')) {
				position++
			}
			operator := sql[start:position]
			switch operator {
			case "=", "<", ">", "<=", ">=", "<>", "!=":
			default:
				return nil, fmt.Errorf("operator %q is unsupported", operator)
			}
			if err := appendToken(oracleTokenOperator, operator); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("character %q is unsupported", current)
		}
	}
	tokens = append(tokens, oracleToken{kind: oracleTokenEOF})
	return tokens, nil
}

func (parser *oracleSelectParser) parseSelect() error {
	if !parser.consumeKeyword("SELECT") {
		return errors.New("only SELECT is supported")
	}
	parser.consumeKeyword("DISTINCT")
	if parser.consumeKeyword("TOP") {
		if parser.dialect != dmDialect && parser.dialect != sqlserverDialect {
			return errors.New("TOP is unsupported for Oracle")
		}
		if err := parser.consumeUnsignedInteger("TOP row count"); err != nil {
			return err
		}
		parser.hasPagination = true
		parser.hasTop = true
	}
	if err := parser.parseProjections(); err != nil {
		return err
	}
	if !parser.consumeKeyword("FROM") {
		return errors.New("FROM with a physical relation is required")
	}
	if err := parser.parseFrom(); err != nil {
		return err
	}
	if parser.consumeKeyword("WHERE") {
		parser.allowRownum = true
		truth, err := parser.parsePredicate()
		parser.allowRownum = false
		if err != nil {
			return fmt.Errorf("WHERE predicate: %w", err)
		}
		parser.hasPredicate = true
		parser.whereTruth = truth
	}
	if parser.consumeKeyword("ORDER") {
		if !parser.consumeKeyword("BY") {
			return errors.New("ORDER must be followed by BY")
		}
		if err := parser.parseOrderBy(); err != nil {
			return err
		}
	}
	if err := parser.parsePagination(); err != nil {
		return err
	}
	if parser.current().kind != oracleTokenEOF {
		return fmt.Errorf("unsupported structure at %q", parser.current().text)
	}
	return nil
}

func (parser *oracleSelectParser) parseProjections() error {
	for {
		if len(parser.projections) >= lineageMaxProjections {
			return fmt.Errorf("projection slots exceed limit %d", lineageMaxProjections)
		}
		projection, err := parser.parseProjection()
		if err != nil {
			return err
		}
		parser.projections = append(parser.projections, projection)
		if !parser.consumeKind(oracleTokenComma) {
			break
		}
	}
	return nil
}

func (parser *oracleSelectParser) parseProjection() (oracleProjection, error) {
	if parser.consumeKind(oracleTokenStar) {
		return oracleProjection{star: true}, nil
	}
	current := parser.current()
	if oracleLiteralToken(current) {
		parser.position++
		projection := oracleProjection{constant: true}
		if alias, ok, err := parser.parseOptionalAlias(); err != nil {
			return oracleProjection{}, err
		} else if ok {
			projection.alias = alias.name
			projection.aliasQuoted = alias.quoted
		}
		return projection, nil
	}
	parts, star, err := parser.parseIdentifierPath(3, true)
	if err != nil {
		return oracleProjection{}, fmt.Errorf("projection: %w", err)
	}
	projection := oracleProjection{star: star}
	if star {
		projection.starQualifier = parts
	} else {
		projection.column = &oracleColumnRef{parts: parts}
		parser.references = append(parser.references, *projection.column)
		parser.columns.add(parts[len(parts)-1].name)
	}
	if alias, ok, aliasErr := parser.parseOptionalAlias(); aliasErr != nil {
		return oracleProjection{}, aliasErr
	} else if ok {
		if star {
			return oracleProjection{}, errors.New("wildcard aliases are unsupported")
		}
		projection.alias = alias.name
		projection.aliasQuoted = alias.quoted
	}
	return projection, nil
}

func (parser *oracleSelectParser) parseOptionalAlias() (oracleIdentifier, bool, error) {
	if parser.consumeKeyword("AS") {
		identifier, err := parser.consumeIdentifier("projection alias")
		return identifier, err == nil, err
	}
	if !oracleTokenCanBeAlias(parser.current()) {
		return oracleIdentifier{}, false, nil
	}
	identifier, err := parser.consumeIdentifier("projection alias")
	return identifier, err == nil, err
}

func (parser *oracleSelectParser) parseFrom() error {
	relation, err := parser.parseRelation()
	if err != nil {
		return err
	}
	parser.relations = append(parser.relations, relation)
	for {
		if parser.consumeKind(oracleTokenComma) {
			next, relationErr := parser.parseRelation()
			if relationErr != nil {
				return relationErr
			}
			parser.relations = append(parser.relations, next)
			continue
		}
		joinKind, joined := parser.consumeJoinKind()
		if !joined {
			break
		}
		next, relationErr := parser.parseRelation()
		if relationErr != nil {
			return relationErr
		}
		parser.relations = append(parser.relations, next)
		if bindingErr := parser.validateRelationBindings(); bindingErr != nil {
			return bindingErr
		}
		if joinKind == "CROSS" {
			continue
		}
		if !parser.consumeKeyword("ON") {
			return errors.New("JOIN requires an ON predicate")
		}
		referenceStart := len(parser.references)
		if _, predicateErr := parser.parsePredicate(); predicateErr != nil {
			return fmt.Errorf("JOIN ON predicate: %w", predicateErr)
		}
		for _, reference := range parser.references[referenceStart:] {
			if _, _, referenceErr := parser.resolveColumnRelations(reference.parts); referenceErr != nil {
				return fmt.Errorf("JOIN ON predicate: %w", referenceErr)
			}
		}
		parser.hasPredicate = true
	}
	return parser.validateRelationBindings()
}

func (parser *oracleSelectParser) parseRelation() (oracleRelation, error) {
	parts, star, err := parser.parseIdentifierPath(2, false)
	if err != nil {
		return oracleRelation{}, fmt.Errorf("FROM relation: %w", err)
	}
	if star || len(parts) == 0 {
		return oracleRelation{}, errors.New("invalid FROM relation")
	}
	relation := oracleRelation{name: parts[len(parts)-1]}
	if len(parts) == 2 {
		relation.schema = parts[0]
		relation.hasSchema = true
	}
	relation.object = model.ObjectRef{Table: relation.name.name}
	if relation.hasSchema {
		relation.object.Schema = relation.schema.name
	}
	if parser.consumeKeyword("AS") {
		if parser.dialect == sqlserverDialect {
			alias, aliasErr := parser.consumeIdentifier("table alias")
			if aliasErr != nil {
				return oracleRelation{}, aliasErr
			}
			relation.alias = alias
			relation.hasAlias = true
			relation.object.Alias = alias.name
			return relation, nil
		}
		return oracleRelation{}, errors.New("AS table aliases are outside the Oracle-compatible profile")
	}
	if oracleTokenCanBeAlias(parser.current()) {
		alias, aliasErr := parser.consumeIdentifier("table alias")
		if aliasErr != nil {
			return oracleRelation{}, aliasErr
		}
		relation.alias = alias
		relation.hasAlias = true
		relation.object.Alias = alias.name
	}
	return relation, nil
}

func (parser *oracleSelectParser) consumeJoinKind() (string, bool) {
	if parser.consumeKeyword("JOIN") {
		return "INNER", true
	}
	for _, kind := range []string{"INNER", "LEFT", "RIGHT", "FULL", "CROSS"} {
		if !parser.consumeKeyword(kind) {
			continue
		}
		if kind != "INNER" && kind != "CROSS" {
			parser.consumeKeyword("OUTER")
		}
		if !parser.consumeKeyword("JOIN") {
			parser.position--
			return "", false
		}
		return kind, true
	}
	return "", false
}

func (parser *oracleSelectParser) parsePredicate() (oracleTruth, error) {
	truth, err := parser.parsePredicateAnd()
	if err != nil {
		return oracleTruthUnknown, err
	}
	hasOr := false
	for parser.consumeKeyword("OR") {
		hasOr = true
		right, rightErr := parser.parsePredicateAnd()
		if rightErr != nil {
			return oracleTruthUnknown, rightErr
		}
		truth = oracleTruthOr(truth, right)
	}
	if hasOr && parser.hasRownum {
		return oracleTruthUnknown, errors.New("ROWNUM with OR is outside the supported profile")
	}
	return truth, nil
}

func (parser *oracleSelectParser) parsePredicateAnd() (oracleTruth, error) {
	truth, err := parser.parseComparison()
	if err != nil {
		return oracleTruthUnknown, err
	}
	for parser.consumeKeyword("AND") {
		right, rightErr := parser.parseComparison()
		if rightErr != nil {
			return oracleTruthUnknown, rightErr
		}
		truth = oracleTruthAnd(truth, right)
	}
	return truth, nil
}

func (parser *oracleSelectParser) parseComparison() (oracleTruth, error) {
	if parser.matchKeyword("ROWNUM") && parser.dialect == oracleDialect && parser.allowRownum {
		parser.position++
		operator := parser.current()
		if operator.kind != oracleTokenOperator || (operator.text != "<" && operator.text != "<=" && operator.text != "=") {
			return oracleTruthUnknown, errors.New("ROWNUM supports only <, <=, or = in WHERE")
		}
		parser.position++
		if err := parser.consumeUnsignedInteger("ROWNUM bound"); err != nil {
			return oracleTruthUnknown, err
		}
		if parser.hasRownum {
			return oracleTruthUnknown, errors.New("multiple ROWNUM predicates are unsupported")
		}
		parser.hasRownum = true
		parser.hasPagination = true
		return oracleTruthUnknown, nil
	}
	left, err := parser.parseOperand()
	if err != nil {
		return oracleTruthUnknown, err
	}
	if parser.consumeKeyword("IS") {
		not := parser.consumeKeyword("NOT")
		if !parser.consumeKeyword("NULL") {
			return oracleTruthUnknown, errors.New("IS supports only NULL")
		}
		if left.column != nil {
			return oracleTruthUnknown, nil
		}
		if parser.dialect == dmDialect && oracleEmptyStringLiteral(left) {
			return oracleTruthUnknown, nil
		}
		truth := oracleTruthFalse
		if oracleNullLiteral(left, parser.dialect) {
			truth = oracleTruthTrue
		}
		if not {
			if truth == oracleTruthTrue {
				return oracleTruthFalse, nil
			}
			return oracleTruthTrue, nil
		}
		return truth, nil
	}
	operator := parser.current()
	if operator.kind != oracleTokenOperator && !parser.matchKeyword("LIKE") {
		return oracleTruthUnknown, errors.New("simple predicate requires a comparison operator")
	}
	parser.position++
	right, err := parser.parseOperand()
	if err != nil {
		return oracleTruthUnknown, err
	}
	return evaluateOracleLiteralComparison(left, operator.text, right, parser.dialect), nil
}

func (parser *oracleSelectParser) parseOperand() (oracleOperand, error) {
	current := parser.current()
	if oracleLiteralToken(current) {
		parser.position++
		return oracleOperand{literalKind: current.kind, literal: current.text}, nil
	}
	parts, star, err := parser.parseIdentifierPath(3, false)
	if err != nil {
		return oracleOperand{}, err
	}
	if star {
		return oracleOperand{}, errors.New("wildcard is invalid in a predicate")
	}
	parser.columns.add(parts[len(parts)-1].name)
	column := oracleColumnRef{parts: parts}
	parser.references = append(parser.references, column)
	return oracleOperand{column: &column}, nil
}

func (parser *oracleSelectParser) parseOrderBy() error {
	for {
		if parser.current().kind == oracleTokenNumber {
			ordinal, err := strconv.ParseUint(parser.current().text, 10, 32)
			if err != nil || ordinal == 0 || ordinal > uint64(len(parser.projections)) {
				return errors.New("ORDER BY ordinal is outside the projection list")
			}
			parser.position++
		} else {
			parts, star, err := parser.parseIdentifierPath(3, false)
			if err != nil || star {
				return errors.New("ORDER BY supports only direct columns")
			}
			aliasMatches := 0
			if len(parts) == 1 {
				for _, projection := range parser.projections {
					if projection.alias != "" && oracleIdentifiersEqual(
						oracleIdentifier{name: projection.alias, quoted: projection.aliasQuoted}, parts[0]) {
						aliasMatches++
					}
				}
			}
			if aliasMatches > 1 {
				return errors.New("ORDER BY alias is ambiguous")
			}
			if aliasMatches == 0 {
				parser.references = append(parser.references, oracleColumnRef{parts: parts})
				parser.columns.add(parts[len(parts)-1].name)
			}
		}
		if !parser.consumeKeyword("ASC") {
			parser.consumeKeyword("DESC")
		}
		if !parser.consumeKind(oracleTokenComma) {
			return nil
		}
	}
}

func (parser *oracleSelectParser) parsePagination() error {
	if parser.hasTop && (parser.matchKeyword("LIMIT") || parser.matchKeyword("OFFSET") || parser.matchKeyword("FETCH")) {
		return errors.New("TOP cannot be combined with trailing pagination")
	}
	if parser.consumeKeyword("LIMIT") {
		if parser.dialect != dmDialect {
			return errors.New("LIMIT is unsupported for Oracle")
		}
		if err := parser.consumeUnsignedInteger("LIMIT row count or offset"); err != nil {
			return err
		}
		if parser.consumeKind(oracleTokenComma) {
			if err := parser.consumeUnsignedInteger("LIMIT row count"); err != nil {
				return err
			}
		} else if parser.consumeKeyword("OFFSET") {
			if err := parser.consumeUnsignedInteger("LIMIT offset"); err != nil {
				return err
			}
		}
		parser.hasPagination = true
		return nil
	}
	if parser.consumeKeyword("OFFSET") {
		if err := parser.consumeUnsignedInteger("OFFSET row count"); err != nil {
			return err
		}
		if !parser.consumeKeyword("ROW") && !parser.consumeKeyword("ROWS") {
			return errors.New("OFFSET row count requires ROW or ROWS")
		}
		parser.hasPagination = true
	}
	if parser.consumeKeyword("FETCH") {
		if !parser.consumeKeyword("FIRST") && !parser.consumeKeyword("NEXT") {
			return errors.New("FETCH requires FIRST or NEXT")
		}
		if err := parser.consumeUnsignedInteger("FETCH row count"); err != nil {
			return err
		}
		if !parser.consumeKeyword("ROW") && !parser.consumeKeyword("ROWS") {
			return errors.New("FETCH row count requires ROW or ROWS")
		}
		if !parser.consumeKeyword("ONLY") {
			return errors.New("FETCH requires ONLY")
		}
		parser.hasPagination = true
	}
	return nil
}

func (parser *oracleSelectParser) consumeUnsignedInteger(label string) error {
	token := parser.current()
	if token.kind != oracleTokenNumber || strings.ContainsRune(token.text, '.') {
		return fmt.Errorf("%s must be an unsigned integer", label)
	}
	if _, err := strconv.ParseUint(token.text, 10, 63); err != nil {
		return fmt.Errorf("%s is out of range", label)
	}
	parser.position++
	return nil
}

func (parser *oracleSelectParser) parseIdentifierPath(maxParts int, allowStar bool) ([]oracleIdentifier, bool, error) {
	first, err := parser.consumeIdentifier("identifier")
	if err != nil {
		return nil, false, err
	}
	parts := []oracleIdentifier{first}
	for parser.consumeKind(oracleTokenDot) {
		if allowStar && parser.consumeKind(oracleTokenStar) {
			return parts, true, nil
		}
		if len(parts) >= maxParts {
			return nil, false, fmt.Errorf("identifier path exceeds %d parts", maxParts)
		}
		next, nextErr := parser.consumeIdentifier("qualified identifier")
		if nextErr != nil {
			return nil, false, nextErr
		}
		parts = append(parts, next)
	}
	return parts, false, nil
}

func (parser *oracleSelectParser) consumeIdentifier(label string) (oracleIdentifier, error) {
	token := parser.current()
	switch token.kind {
	case oracleTokenQuotedIdentifier:
		parser.position++
		return oracleIdentifier{name: token.text, quoted: true}, nil
	case oracleTokenWord:
		if (parser.dialect == oracleDialect || parser.dialect == dmDialect) && strings.EqualFold(token.text, "ROWNUM") {
			return oracleIdentifier{}, errors.New("unquoted ROWNUM requires a supported Oracle WHERE bound")
		}
		if oracleReservedKeyword(token.text) {
			return oracleIdentifier{}, fmt.Errorf("%s cannot be reserved keyword %q", label, token.text)
		}
		parser.position++
		return oracleIdentifier{name: token.text}, nil
	default:
		return oracleIdentifier{}, fmt.Errorf("%s expected, got %q", label, token.text)
	}
}

func (parser *oracleSelectParser) validateRelationBindings() error {
	for left := range parser.relations {
		leftName := parser.relations[left].name
		if parser.relations[left].hasAlias {
			leftName = parser.relations[left].alias
		}
		for right := left + 1; right < len(parser.relations); right++ {
			rightName := parser.relations[right].name
			if parser.relations[right].hasAlias {
				rightName = parser.relations[right].alias
			}
			if oracleIdentifiersEqual(leftName, rightName) {
				return fmt.Errorf("duplicate visible relation name %q", leftName.name)
			}
		}
	}
	return nil
}

func (parser *oracleSelectParser) buildAST(sql string) (*model.AST, error) {
	for _, reference := range parser.references {
		if _, _, err := parser.resolveColumnRelations(reference.parts); err != nil {
			return nil, err
		}
	}
	lineages := make([]model.ProjectionLineage, 0, len(parser.projections))
	for index, projection := range parser.projections {
		lineage := model.ProjectionLineage{SelectIndex: index, OutputName: projection.alias}
		switch {
		case projection.constant:
			lineage.Arms = []model.LineageArm{{
				Kind: model.LineageConstant, Operation: "constant", Status: model.LineageSourceFree,
			}}
		case projection.star:
			lineage.Variadic = true
			arm, err := parser.wildcardLineage(projection.starQualifier)
			if err != nil {
				return nil, err
			}
			lineage.Arms = []model.LineageArm{arm}
		case projection.column != nil:
			if lineage.OutputName == "" {
				lineage.OutputName = projection.column.parts[len(projection.column.parts)-1].name
			}
			arm, err := parser.columnLineage(*projection.column)
			if err != nil {
				return nil, err
			}
			lineage.Arms = []model.LineageArm{arm}
		default:
			return nil, errors.New("projection has no supported lineage shape")
		}
		lineages = append(lineages, lineage)
	}
	if err := validateLineageLimits(lineages); err != nil {
		return nil, err
	}
	tables := make(objectSet)
	for _, relation := range parser.relations {
		tables.add(relation.object)
	}
	operations := stringSet{
		"SELECT":                     {},
		nestingDepthOperation + ":0": {},
		unionCountOperation + ":0":   {},
	}
	for _, projection := range parser.projections {
		if projection.column != nil {
			operations.add(selectColumnOperation + ":" + projection.column.parts[len(projection.column.parts)-1].name)
		} else if projection.star {
			operations.add(selectColumnOperation + ":*")
		}
	}
	return &model.AST{
		Dialect:            parser.dialect,
		RawSQL:             sql,
		Normalized:         normalizeOracleCompatible(parser.tokens),
		StmtType:           model.StmtType("SELECT"),
		Tables:             tables.sorted(),
		Columns:            parser.columns.sorted(),
		DirectProjections:  deriveDirectProjections(lineages),
		ProjectionLineages: lineages,
		HasWhere:           parser.hasPredicate,
		WhereTautology:     parser.whereTruth == oracleTruthTrue,
		HasLimit:           parser.hasPagination,
		Operations:         operations.sorted(),
	}, nil
}

func (parser *oracleSelectParser) columnLineage(column oracleColumnRef) (model.LineageArm, error) {
	relations, status, err := parser.resolveColumnRelations(column.parts)
	if err != nil {
		return model.LineageArm{}, err
	}
	arm := model.LineageArm{
		Kind: model.LineageDirect, Operation: "column", Status: status,
		PossibleRelations: relations,
	}
	if status == model.LineageResolved {
		arm.Dependencies = []model.ColumnDependency{{
			Origin: model.ColumnOrigin{Relation: relations[0], Column: column.parts[len(column.parts)-1].name},
			Role:   model.DependencyValue,
		}}
	}
	return arm, nil
}

func (parser *oracleSelectParser) wildcardLineage(qualifiers []oracleIdentifier) (model.LineageArm, error) {
	var relations []model.ObjectRef
	if len(qualifiers) == 0 {
		relations = parser.physicalRelations(parser.relations)
	} else {
		matched, err := parser.resolveQualifiedRelations(qualifiers)
		if err != nil {
			return model.LineageArm{}, err
		}
		relations = parser.physicalRelations(matched)
	}
	if len(relations) == 0 {
		return model.LineageArm{}, errors.New("wildcard has no physical source relation")
	}
	if len(relations) != 1 {
		return model.LineageArm{
			Kind: model.LineageOpaque, Operation: "wildcard_relation_ambiguous",
			Status: model.LineageAmbiguous, PossibleRelations: relations,
		}, nil
	}
	return model.LineageArm{
		Kind: model.LineageWildcard, Operation: "wildcard", Status: model.LineageResolved,
		PossibleRelations: relations,
	}, nil
}

func (parser *oracleSelectParser) resolveColumnRelations(parts []oracleIdentifier) ([]model.ObjectRef, model.LineageStatus, error) {
	switch len(parts) {
	case 1:
		relations := parser.physicalRelations(parser.relations)
		if len(relations) == 0 {
			return nil, model.LineageUnsupported, fmt.Errorf("column %q has no FROM relation", parts[0].name)
		}
		if len(relations) == 1 {
			return relations, model.LineageResolved, nil
		}
		return relations, model.LineageAmbiguous, nil
	case 2, 3:
		matched, err := parser.resolveQualifiedRelations(parts[:len(parts)-1])
		if err != nil {
			return nil, model.LineageUnsupported, err
		}
		relations := parser.physicalRelations(matched)
		if len(relations) != 1 {
			return relations, model.LineageAmbiguous, nil
		}
		return relations, model.LineageResolved, nil
	default:
		return nil, model.LineageUnsupported, errors.New("unsupported column qualification")
	}
}

func (parser *oracleSelectParser) resolveQualifiedRelations(qualifiers []oracleIdentifier) ([]oracleRelation, error) {
	matches := make([]oracleRelation, 0, 1)
	switch len(qualifiers) {
	case 1:
		for _, relation := range parser.relations {
			if relation.hasAlias && oracleIdentifiersEqual(relation.alias, qualifiers[0]) {
				matches = append(matches, relation)
			}
		}
		if len(matches) == 0 {
			for _, relation := range parser.relations {
				if !relation.hasAlias && oracleIdentifiersEqual(relation.name, qualifiers[0]) {
					matches = append(matches, relation)
				}
			}
		}
	case 2:
		for _, relation := range parser.relations {
			if !relation.hasAlias && relation.hasSchema &&
				oracleIdentifiersEqual(relation.schema, qualifiers[0]) &&
				oracleIdentifiersEqual(relation.name, qualifiers[1]) {
				matches = append(matches, relation)
			}
		}
	default:
		return nil, errors.New("unsupported relation qualification")
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("qualifier %q does not resolve to a FROM relation", oracleIdentifierPath(qualifiers))
	}
	return matches, nil
}

func (parser *oracleSelectParser) physicalRelations(relations []oracleRelation) []model.ObjectRef {
	result := make([]model.ObjectRef, 0, len(relations))
	seen := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		object := relation.object
		object.Alias = ""
		key := object.Schema + "\x00" + object.Table
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, object)
	}
	return result
}

func (parser *oracleSelectParser) current() oracleToken {
	if parser.position >= len(parser.tokens) {
		return oracleToken{kind: oracleTokenEOF}
	}
	return parser.tokens[parser.position]
}

func (parser *oracleSelectParser) consumeKind(kind oracleTokenKind) bool {
	if parser.current().kind != kind {
		return false
	}
	parser.position++
	return true
}

func (parser *oracleSelectParser) matchKeyword(keyword string) bool {
	return parser.current().kind == oracleTokenWord && strings.EqualFold(parser.current().text, keyword)
}

func (parser *oracleSelectParser) consumeKeyword(keyword string) bool {
	if !parser.matchKeyword(keyword) {
		return false
	}
	parser.position++
	return true
}

func oracleLiteralToken(token oracleToken) bool {
	if token.kind == oracleTokenString || token.kind == oracleTokenNumber {
		return true
	}
	return token.kind == oracleTokenWord &&
		(strings.EqualFold(token.text, "NULL") || strings.EqualFold(token.text, "TRUE") || strings.EqualFold(token.text, "FALSE"))
}

func oracleTokenCanBeAlias(token oracleToken) bool {
	return token.kind == oracleTokenQuotedIdentifier ||
		(token.kind == oracleTokenWord && !oracleReservedKeyword(token.text))
}

func oracleReservedKeyword(value string) bool {
	switch strings.ToUpper(value) {
	case "SELECT", "DISTINCT", "TOP", "FROM", "WHERE", "AS", "JOIN", "INNER", "LEFT", "RIGHT", "FULL",
		"OUTER", "CROSS", "ON", "AND", "OR", "IS", "NOT", "NULL", "TRUE", "FALSE", "LIKE", "ORDER", "BY",
		"ASC", "DESC", "LIMIT", "OFFSET", "FETCH", "FIRST", "NEXT", "ROW", "ROWS", "ONLY", "GROUP", "HAVING",
		"UNION", "INTERSECT", "MINUS", "EXCEPT", "WITH", "CONNECT", "START", "MODEL", "FOR", "UPDATE", "INSERT",
		"DELETE", "MERGE", "INTO", "CALL", "EXEC", "EXECUTE", "BEGIN", "DECLARE", "CREATE", "ALTER", "DROP",
		"TRUNCATE", "GRANT", "REVOKE", "COMMIT", "ROLLBACK", "SAVEPOINT", "LOCK", "NEXTVAL", "CURRVAL", "CASE",
		"WHEN", "THEN", "ELSE", "END", "NATURAL", "USING":
		return true
	default:
		return false
	}
}

func oracleIdentifiersEqual(left, right oracleIdentifier) bool {
	if left.quoted || right.quoted {
		return left.quoted == right.quoted && left.name == right.name
	}
	return strings.EqualFold(left.name, right.name)
}

func oracleIdentifierPath(parts []oracleIdentifier) string {
	values := make([]string, len(parts))
	for index, part := range parts {
		values[index] = part.name
	}
	return strings.Join(values, ".")
}

func oracleTruthAnd(left, right oracleTruth) oracleTruth {
	if left == oracleTruthFalse || right == oracleTruthFalse {
		return oracleTruthFalse
	}
	if left == oracleTruthTrue && right == oracleTruthTrue {
		return oracleTruthTrue
	}
	return oracleTruthUnknown
}

func oracleTruthOr(left, right oracleTruth) oracleTruth {
	if left == oracleTruthTrue || right == oracleTruthTrue {
		return oracleTruthTrue
	}
	if left == oracleTruthFalse && right == oracleTruthFalse {
		return oracleTruthFalse
	}
	return oracleTruthUnknown
}

func oracleNullLiteral(operand oracleOperand, dialect model.DBDialect) bool {
	return operand.column == nil && (operand.literalKind == oracleTokenWord && strings.EqualFold(operand.literal, "NULL") ||
		dialect == oracleDialect && oracleEmptyStringLiteral(operand))
}

func oracleEmptyStringLiteral(operand oracleOperand) bool {
	return operand.column == nil && operand.literalKind == oracleTokenString && operand.literal == "''"
}

func evaluateOracleLiteralComparison(left oracleOperand, operator string, right oracleOperand, dialect model.DBDialect) oracleTruth {
	if oracleNullLiteral(left, dialect) || oracleNullLiteral(right, dialect) ||
		dialect == dmDialect && (oracleEmptyStringLiteral(left) || oracleEmptyStringLiteral(right)) {
		return oracleTruthUnknown
	}
	if left.column != nil || right.column != nil || strings.EqualFold(operator, "LIKE") ||
		left.literalKind != right.literalKind {
		return oracleTruthUnknown
	}
	equal := left.literal == right.literal
	if left.literalKind == oracleTokenWord {
		equal = strings.EqualFold(left.literal, right.literal)
	}
	switch operator {
	case "=":
		if equal {
			return oracleTruthTrue
		}
		return oracleTruthFalse
	case "!=", "<>":
		if equal {
			return oracleTruthFalse
		}
		return oracleTruthTrue
	default:
		return oracleTruthUnknown
	}
}

func normalizeOracleCompatible(tokens []oracleToken) string {
	values := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token.kind == oracleTokenEOF {
			break
		}
		switch token.kind {
		case oracleTokenString, oracleTokenNumber:
			values = append(values, "?")
		case oracleTokenQuotedIdentifier:
			values = append(values, `"`+strings.ReplaceAll(token.text, `"`, `""`)+`"`)
		case oracleTokenWord:
			if oracleReservedKeyword(token.text) {
				values = append(values, strings.ToUpper(token.text))
			} else {
				values = append(values, token.text)
			}
		default:
			values = append(values, token.text)
		}
	}
	return strings.Join(values, " ")
}
