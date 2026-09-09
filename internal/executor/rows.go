package executor

import (
	"errors"
	"fmt"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

type rowSource interface {
	Columns() ([]string, error)
	Next() bool
	Values() ([]any, error)
	Err() error
	Close() error
}

func collectRows(source rowSource, rowLimit int) (model.QueryResult, error) {
	if isNilValue(source) {
		return model.QueryResult{}, fmt.Errorf("collect rows: source is nil")
	}
	if rowLimit <= 0 {
		return model.QueryResult{}, closeRowSource(
			source,
			fmt.Errorf("collect rows: row limit must be positive"),
		)
	}
	columns, err := source.Columns()
	if err != nil {
		return model.QueryResult{}, fmt.Errorf("collect row columns: %w", closeRowSource(source, err))
	}
	result := model.QueryResult{
		Columns: append([]string{}, columns...),
		Rows:    make([][]string, 0, min(rowLimit, 1_024)),
	}
	for source.Next() {
		values, err := source.Values()
		if err != nil {
			return model.QueryResult{}, fmt.Errorf("read row values: %w", closeRowSource(source, err))
		}
		if len(values) != len(columns) {
			return model.QueryResult{}, fmt.Errorf(
				"read row values: %w",
				closeRowSource(source, fmt.Errorf("column count mismatch")),
			)
		}
		if len(result.Rows) == rowLimit {
			result.Truncated = true
			break
		}
		row := make([]string, len(values))
		for index, value := range values {
			row[index] = stringifyDatabaseValue(value)
		}
		result.Rows = append(result.Rows, row)
	}
	iterationError := source.Err()
	closeError := source.Close()
	if iterationError != nil || closeError != nil {
		return model.QueryResult{}, fmt.Errorf(
			"finish rows: %w",
			errors.Join(iterationError, closeError),
		)
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

func closeRowSource(source rowSource, cause error) error {
	if err := source.Close(); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func stringifyDatabaseValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(typed)
	}
}
