package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	errInvalidTimestampSource = errors.New("unsupported SQLite timestamp source")
	errNullRequiredTimestamp  = errors.New("required SQLite timestamp is null")
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
		timestamp.time = value
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
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	var lastError error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			timestamp.time = parsed
			timestamp.valid = true
			return nil
		}
		lastError = err
	}
	return fmt.Errorf("parse SQLite timestamp %q: %w", value, lastError)
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

func intPointer(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int64)
	return &result
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
