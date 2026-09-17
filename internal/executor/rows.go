package executor

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/jackc/pgx/v5/pgtype"
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
	if text, ok := stringifySupportedDatabaseValue(value); ok {
		return text
	}

	if valuer, ok := value.(driver.Valuer); ok {
		valuerValue, err := safelyReadDriverValue(valuer)
		if err == nil {
			if text, supported := stringifySupportedDatabaseValue(valuerValue); supported {
				return text
			}
		}
	}

	return fmt.Sprint(value)
}

func stringifySupportedDatabaseValue(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case time.Time:
		return typed.Format(time.RFC3339Nano), true
	case pgtype.Numeric:
		return stringifyPGNumeric(typed), true
	case pgtype.Interval:
		return stringifyPGInterval(typed), true
	case pgtype.InfinityModifier:
		return stringifyPGInfinityModifier(typed), true
	case [16]byte:
		return (pgtype.UUID{Bytes: typed, Valid: true}).String(), true
	case map[string]any:
		return stringifyStructuredDatabaseValue(typed)
	case []any:
		return stringifyStructuredDatabaseValue(typed)
	case bool:
		return strconv.FormatBool(typed), true
	case int:
		return strconv.FormatInt(int64(typed), 10), true
	case int8:
		return strconv.FormatInt(int64(typed), 10), true
	case int16:
		return strconv.FormatInt(int64(typed), 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(typed), 10), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32), true
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	default:
		return "", false
	}
}

func stringifyPGNumeric(value pgtype.Numeric) string {
	if !value.Valid {
		return ""
	}
	if value.NaN {
		return "NaN"
	}
	if value.InfinityModifier == pgtype.Infinity {
		return "Infinity"
	}
	if value.InfinityModifier == pgtype.NegativeInfinity {
		return "-Infinity"
	}
	if value.InfinityModifier != pgtype.Finite {
		return "invalid"
	}

	digits := "0"
	if value.Int != nil {
		digits = value.Int.Text(10)
	}
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign = "-"
		digits = strings.TrimPrefix(digits, "-")
	}

	switch {
	case value.Exp > 0:
		return sign + digits + strings.Repeat("0", int(value.Exp))
	case value.Exp == 0:
		return sign + digits
	}

	scale := int(-int64(value.Exp))
	if len(digits) <= scale {
		return sign + "0." + strings.Repeat("0", scale-len(digits)) + digits
	}
	decimalPoint := len(digits) - scale
	return sign + digits[:decimalPoint] + "." + digits[decimalPoint:]
}

func stringifyPGInterval(value pgtype.Interval) string {
	if !value.Valid {
		return ""
	}
	driverValue, err := value.Value()
	if err != nil || driverValue == nil {
		return ""
	}
	return fmt.Sprint(driverValue)
}

func stringifyPGInfinityModifier(value pgtype.InfinityModifier) string {
	switch value {
	case pgtype.Infinity:
		return "Infinity"
	case pgtype.NegativeInfinity:
		return "-Infinity"
	default:
		return ""
	}
}

func stringifyStructuredDatabaseValue(value any) (string, bool) {
	encoded, err := json.Marshal(normalizeStructuredDatabaseValue(value))
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func normalizeStructuredDatabaseValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case pgtype.Numeric:
		if !typed.Valid {
			return nil
		}
		text := stringifyPGNumeric(typed)
		if typed.NaN || typed.InfinityModifier != pgtype.Finite {
			return text
		}
		return json.Number(text)
	case pgtype.Interval:
		if !typed.Valid {
			return nil
		}
		return stringifyPGInterval(typed)
	case pgtype.InfinityModifier:
		return stringifyPGInfinityModifier(typed)
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	case [16]byte:
		return (pgtype.UUID{Bytes: typed, Valid: true}).String()
	case []byte:
		return string(typed)
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeStructuredDatabaseValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeStructuredDatabaseValue(item)
		}
		return normalized
	default:
		return typed
	}
}

func safelyReadDriverValue(valuer driver.Valuer) (value driver.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			value = nil
			err = fmt.Errorf("driver valuer panic: %v", recovered)
		}
	}()
	return valuer.Value()
}
