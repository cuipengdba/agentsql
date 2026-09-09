package executor

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

type sessionClock interface {
	Now() time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now()
}

type transactionAction uint8

const (
	transactionNone transactionAction = iota
	transactionBegin
	transactionEnd
)

type statementClass struct {
	transaction transactionAction
	write       bool
}

type transactionStateMachine struct {
	mu            sync.RWMutex
	clock         sessionClock
	inTransaction bool
	startedAt     time.Time
	lastStmtEnd   time.Time
	affectedRows  int64
}

func newTransactionStateMachine(clock sessionClock) *transactionStateMachine {
	if isNilValue(clock) {
		clock = wallClock{}
	}
	return &transactionStateMachine{clock: clock}
}

func (machine *transactionStateMachine) finishStatement(
	classification statementClass,
	started time.Time,
	affectedRows int64,
	succeeded bool,
) {
	ended := machine.clock.Now()
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if succeeded {
		switch classification.transaction {
		case transactionBegin:
			if !machine.inTransaction {
				machine.inTransaction = true
				machine.startedAt = started
				machine.affectedRows = 0
			}
		case transactionEnd:
			machine.clearLocked()
			return
		}
		if machine.inTransaction && classification.write && affectedRows > 0 {
			if machine.affectedRows > math.MaxInt64-affectedRows {
				machine.affectedRows = math.MaxInt64
			} else {
				machine.affectedRows += affectedRows
			}
		}
	}
	if machine.inTransaction {
		machine.lastStmtEnd = ended
	}
}

func (machine *transactionStateMachine) touchStatementEnd() {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	if machine.inTransaction {
		machine.lastStmtEnd = machine.clock.Now()
	}
}

func (machine *transactionStateMachine) snapshot() (
	inTransaction bool,
	ageMS int64,
	idleMS int64,
	affectedRows int64,
	err error,
) {
	machine.mu.RLock()
	defer machine.mu.RUnlock()
	if !machine.inTransaction {
		return false, 0, 0, 0, nil
	}
	now := machine.clock.Now()
	if machine.startedAt.IsZero() || machine.lastStmtEnd.IsZero() ||
		now.Before(machine.startedAt) || now.Before(machine.lastStmtEnd) {
		return false, 0, 0, 0, fmt.Errorf("transaction clock moved backwards")
	}
	return true,
		now.Sub(machine.startedAt).Milliseconds(),
		now.Sub(machine.lastStmtEnd).Milliseconds(),
		machine.affectedRows,
		nil
}

func (machine *transactionStateMachine) active() bool {
	machine.mu.RLock()
	defer machine.mu.RUnlock()
	return machine.inTransaction
}

func (machine *transactionStateMachine) clear() {
	machine.mu.Lock()
	defer machine.mu.Unlock()
	machine.clearLocked()
}

func (machine *transactionStateMachine) clearLocked() {
	machine.inTransaction = false
	machine.startedAt = time.Time{}
	machine.lastStmtEnd = time.Time{}
	machine.affectedRows = 0
}

func classifySessionStatement(sqlText string, dialect string) (statementClass, error) {
	tokens, err := transactionTokens(sqlText)
	if err != nil {
		return statementClass{}, err
	}
	if len(tokens) == 0 {
		return statementClass{}, fmt.Errorf("SQL has no statement keyword")
	}
	classification := statementClass{}
	switch tokens[0] {
	case "BEGIN":
		classification.transaction = transactionBegin
	case "START":
		if len(tokens) > 1 && tokens[1] == "TRANSACTION" {
			classification.transaction = transactionBegin
		}
	case "COMMIT", "END", "ABORT":
		classification.transaction = transactionEnd
	case "ROLLBACK":
		if !containsToken(tokens[1:], "TO") {
			classification.transaction = transactionEnd
		}
	case "SET":
		if strings.EqualFold(dialect, "mysql") {
			classification.transaction = mysqlAutocommitAction(tokens)
		}
	case "INSERT", "UPDATE", "DELETE", "MERGE":
		classification.write = true
	}
	return classification, nil
}

func mysqlAutocommitAction(tokens []string) transactionAction {
	if containsToken(tokens, "GLOBAL") || len(tokens) < 3 || tokens[0] != "SET" {
		return transactionNone
	}
	autocommit := -1
	for index, token := range tokens {
		if token == "AUTOCOMMIT" {
			autocommit = index
			break
		}
	}
	if autocommit < 0 {
		return transactionNone
	}
	for index := autocommit + 1; index < len(tokens); index++ {
		switch tokens[index] {
		case "0", "OFF":
			return transactionBegin
		case "1", "ON":
			return transactionEnd
		}
	}
	return transactionNone
}

func containsToken(tokens []string, target string) bool {
	for _, token := range tokens {
		if token == target {
			return true
		}
	}
	return false
}

func transactionTokens(sqlText string) ([]string, error) {
	tokens := make([]string, 0, 8)
	for index := 0; index < len(sqlText); {
		character := sqlText[index]
		switch {
		case isSQLSpace(character):
			index++
		case character == '-' && index+1 < len(sqlText) && sqlText[index+1] == '-':
			index += 2
			for index < len(sqlText) && sqlText[index] != '\n' && sqlText[index] != '\r' {
				index++
			}
		case character == '#':
			index++
			for index < len(sqlText) && sqlText[index] != '\n' && sqlText[index] != '\r' {
				index++
			}
		case character == '/' && index+1 < len(sqlText) && sqlText[index+1] == '*':
			end := strings.Index(sqlText[index+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated SQL block comment")
			}
			index += end + 4
		case character == '\'' || character == '"' || character == '`':
			next, err := skipSQLQuoted(sqlText, index, character)
			if err != nil {
				return nil, err
			}
			index = next
		case isSQLWord(character):
			start := index
			for index < len(sqlText) && isSQLWord(sqlText[index]) {
				index++
			}
			tokens = append(tokens, strings.ToUpper(sqlText[start:index]))
		case character == ';':
			return tokens, nil
		default:
			index++
		}
	}
	return tokens, nil
}

func skipSQLQuoted(sqlText string, start int, quote byte) (int, error) {
	for index := start + 1; index < len(sqlText); index++ {
		if sqlText[index] == '\\' {
			index++
			continue
		}
		if sqlText[index] != quote {
			continue
		}
		if index+1 < len(sqlText) && sqlText[index+1] == quote {
			index++
			continue
		}
		return index + 1, nil
	}
	return 0, fmt.Errorf("unterminated SQL quoted value")
}

func isSQLSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func isSQLWord(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		character == '_'
}
