package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	errInvalidTimestampSource = errors.New("unsupported metadata timestamp source")
	errNullRequiredTimestamp  = errors.New("required metadata timestamp is null")
	errInvalidBooleanSource   = errors.New("unsupported metadata boolean source")
)

type rowScanner interface {
	Scan(dest ...any) error
}

type databaseTimestamp struct {
	time  time.Time
	valid bool
}

func (timestamp *databaseTimestamp) Scan(source any) error {
	if source == nil {
		timestamp.time = time.Time{}
		timestamp.valid = false
		return nil
	}

	switch value := source.(type) {
	case time.Time:
		timestamp.time = value.UTC()
		timestamp.valid = true
		return nil
	case string:
		return timestamp.parse(value)
	case []byte:
		return timestamp.parse(string(value))
	default:
		return fmt.Errorf("scan timestamp source %T: %w", source, errInvalidTimestampSource)
	}
}

func (timestamp *databaseTimestamp) parse(value string) error {
	fields := strings.Fields(value)
	if len(fields) == 4 {
		withoutZoneName := strings.Join(fields[:3], " ")
		for _, layout := range []string{
			"2006-01-02 15:04:05.999999999 -0700",
			"2006-01-02 15:04:05 -0700",
		} {
			parsed, err := time.Parse(layout, withoutZoneName)
			if err == nil {
				timestamp.time = parsed.UTC()
				timestamp.valid = true
				return nil
			}
		}
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	var lastError error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			timestamp.time = parsed.UTC()
			timestamp.valid = true
			return nil
		}
		lastError = err
	}
	return fmt.Errorf("parse metadata timestamp %q: %w", value, lastError)
}

func (timestamp databaseTimestamp) required(field string) (time.Time, error) {
	if !timestamp.valid {
		return time.Time{}, fmt.Errorf("read %s: %w", field, errNullRequiredTimestamp)
	}
	return timestamp.time, nil
}

func (timestamp databaseTimestamp) pointer() *time.Time {
	if !timestamp.valid {
		return nil
	}
	value := timestamp.time
	return &value
}

func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func intPointer(value sql.NullInt64) (*int, error) {
	if !value.Valid {
		return nil, nil
	}
	if value.Int64 < int64(math.MinInt) || value.Int64 > int64(math.MaxInt) {
		return nil, fmt.Errorf("convert metadata integer %d to int: overflow", value.Int64)
	}
	result := int(value.Int64)
	return &result, nil
}

type databaseBool struct {
	value bool
}

func (boolean *databaseBool) Scan(source any) error {
	switch value := source.(type) {
	case bool:
		boolean.value = value
		return nil
	case int64:
		return boolean.scanInteger(value)
	case string:
		return boolean.scanString(value)
	case []byte:
		return boolean.scanString(string(value))
	default:
		return fmt.Errorf("scan boolean source %T: %w", source, errInvalidBooleanSource)
	}
}

func (boolean *databaseBool) scanInteger(value int64) error {
	switch value {
	case 0:
		boolean.value = false
	case 1:
		boolean.value = true
	default:
		return fmt.Errorf("scan boolean integer %d: %w", value, errInvalidBooleanSource)
	}
	return nil
}

func (boolean *databaseBool) scanString(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false":
		boolean.value = false
	case "1", "true":
		boolean.value = true
	default:
		if parsed, err := strconv.ParseBool(value); err == nil {
			boolean.value = parsed
			return nil
		}
		return fmt.Errorf("scan boolean value %q: %w", value, errInvalidBooleanSource)
	}
	return nil
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
