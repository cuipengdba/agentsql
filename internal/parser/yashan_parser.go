package parser

import (
	"errors"

	"github.com/cuipengdba/agentsql/internal/model"
)

// yashanParser accepts only the Oracle-compatible SELECT subset that the
// existing strict parser can prove. It does not infer support for Yashan's
// other Oracle or PostgreSQL compatibility syntax.
type yashanParser struct{}

var _ Parser = (*yashanParser)(nil)

// NewYashanParser constructs the narrow, offline Yashan dialect parser.
func NewYashanParser() Parser { return &yashanParser{} }

func (parser *yashanParser) Parse(sql string) (ast *model.AST, err error) {
	const dialect = model.DialectYashan
	blank := func() *model.AST { return &model.AST{Dialect: dialect, RawSQL: sql} }
	defer func() {
		if recovered := recover(); recovered != nil {
			ast = blank()
			err = recoveredError(dialect, recovered)
		}
	}()

	// The Oracle parser checks input size, UTF-8, token count, grammar, and
	// lineage limits before returning an AST. The reused NULL and ROWNUM
	// rules are exercised by the offline Yashan profile tests.
	result, parseErr := (&oracleCompatibleParser{dialect: oracleDialect}).Parse(sql)
	if parseErr != nil {
		return blank(), unparseableError(dialect, errors.New("outside the supported Yashan SELECT profile"))
	}

	// Oracle's narrow grammar permits arbitrary unquoted words as aliases.
	// Reject words that might instead introduce unsupported syntax. Quoted
	// identifiers remain unambiguous and are not subject to this check.
	tokens, tokenErr := tokenizeOracleCompatible(sql, oracleDialect)
	if tokenErr != nil {
		return blank(), unparseableError(dialect, tokenErr)
	}
	for _, token := range tokens {
		if token.kind == oracleTokenWord && yashanUnsupportedWord(token.text) {
			return blank(), unparseableError(dialect, errors.New("unsupported Yashan syntax marker"))
		}
	}
	result.Dialect = dialect
	return result, nil
}
