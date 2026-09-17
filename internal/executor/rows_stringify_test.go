package executor

import (
	"database/sql/driver"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestStringifyDatabaseValueNumeric(t *testing.T) {
	tests := []struct {
		name  string
		value pgtype.Numeric
		want  string
	}{
		{name: "decimal 22.74", value: numericValue(2274, -2), want: "22.74"},
		{name: "preserves trailing zero", value: numericValue(2270, -2), want: "22.70"},
		{name: "leading zeros", value: numericValue(12, -4), want: "0.0012"},
		{name: "positive exponent", value: numericValue(12, 2), want: "1200"},
		{name: "integer", value: numericValue(7, 0), want: "7"},
		{name: "negative", value: numericValue(-750, -2), want: "-7.50"},
		{
			name:  "large exact decimal",
			value: numericTextValue("12345678901234567890123456789012", -2),
			want:  "123456789012345678901234567890.12",
		},
		{name: "finite zero without coefficient", value: pgtype.Numeric{Exp: -2, Valid: true}, want: "0.00"},
		{name: "null", value: pgtype.Numeric{Valid: false}, want: ""},
		{name: "NaN", value: pgtype.Numeric{NaN: true, Valid: true}, want: "NaN"},
		{name: "positive infinity", value: pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}, want: "Infinity"},
		{name: "negative infinity", value: pgtype.Numeric{InfinityModifier: pgtype.NegativeInfinity, Valid: true}, want: "-Infinity"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, stringifyPGNumeric(test.value))
		})
	}

	require.Equal(t, "22.74", stringifyDatabaseValue(numericValue(2274, -2)))
}

func TestStringifyDatabaseValueScalars(t *testing.T) {
	timestamp := time.Date(2026, time.September, 17, 8, 9, 10, 123456789, time.FixedZone("UTC+8", 8*60*60))
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "nil", value: nil, want: ""},
		{name: "bool", value: true, want: "true"},
		{name: "int", value: int(-1), want: "-1"},
		{name: "int8", value: int8(-8), want: "-8"},
		{name: "int16", value: int16(-16), want: "-16"},
		{name: "int32", value: int32(-32), want: "-32"},
		{name: "int64", value: int64(-64), want: "-64"},
		{name: "uint", value: uint(1), want: "1"},
		{name: "uint8", value: uint8(8), want: "8"},
		{name: "uint16", value: uint16(16), want: "16"},
		{name: "uint32", value: uint32(32), want: "32"},
		{name: "uint64", value: uint64(64), want: "64"},
		{name: "uintptr", value: uintptr(128), want: "128"},
		{name: "float32", value: float32(1.25), want: "1.25"},
		{name: "float64", value: float64(3.5), want: "3.5"},
		{name: "MySQL decimal bytes", value: []byte("22.74"), want: "22.74"},
		{name: "string", value: "text", want: "text"},
		{name: "time", value: timestamp, want: "2026-09-17T08:09:10.123456789+08:00"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, stringifyDatabaseValue(test.value))
		})
	}
}

func TestStringifyDatabaseValueUsesDriverValuerOnce(t *testing.T) {
	uuid := pgtype.UUID{
		Bytes: [16]byte{0x12, 0x34, 0x56, 0x78, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78},
		Valid: true,
	}
	require.Equal(t, "12345678-1234-5678-90ab-cdef12345678", stringifyDatabaseValue(uuid))
	require.Equal(t, "", stringifyDatabaseValue(pgtype.UUID{}))

	require.Equal(t, "valuer fallback", stringifyDatabaseValue(failingTestValuer{}))
	require.Equal(t, "recursive valuer", stringifyDatabaseValue(recursiveTestValuer{}))
	require.Equal(t, "panicking valuer", stringifyDatabaseValue(panickingTestValuer{}))
}

func TestStringifyDatabaseValuePGXStructuredTypes(t *testing.T) {
	uuidBytes := [16]byte{
		0x12, 0x34, 0x56, 0x78, 0x12, 0x34, 0x56, 0x78,
		0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78,
	}
	interval := pgtype.Interval{
		Microseconds: int64(2*time.Hour/time.Microsecond) +
			int64(3*time.Minute/time.Microsecond) +
			int64(4*time.Second/time.Microsecond) + 5_000,
		Days:  1,
		Valid: true,
	}
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "uuid bytes returned by Rows Values", value: uuidBytes, want: "12345678-1234-5678-90ab-cdef12345678"},
		{name: "interval", value: interval, want: "1 day 02:03:04.005000"},
		{name: "null interval", value: pgtype.Interval{}, want: ""},
		{name: "positive time infinity", value: pgtype.Infinity, want: "Infinity"},
		{name: "negative time infinity", value: pgtype.NegativeInfinity, want: "-Infinity"},
		{
			name:  "json object",
			value: map[string]any{"amount": 22.74, "active": true},
			want:  `{"active":true,"amount":22.74}`,
		},
		{
			name:  "array recursively formats pgx values",
			value: []any{numericValue(2274, -2), uuidBytes, interval},
			want:  `[22.74,"12345678-1234-5678-90ab-cdef12345678","1 day 02:03:04.005000"]`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, stringifyDatabaseValue(test.value))
			require.NotContains(t, stringifyDatabaseValue(test.value), "finite")
		})
	}
}

func numericValue(coefficient int64, exponent int32) pgtype.Numeric {
	return pgtype.Numeric{
		Int:   big.NewInt(coefficient),
		Exp:   exponent,
		Valid: true,
	}
}

func numericTextValue(coefficient string, exponent int32) pgtype.Numeric {
	integer, ok := new(big.Int).SetString(coefficient, 10)
	if !ok {
		panic("invalid numeric test coefficient")
	}
	return pgtype.Numeric{Int: integer, Exp: exponent, Valid: true}
}

type failingTestValuer struct{}

func (failingTestValuer) Value() (driver.Value, error) {
	return nil, errors.New("value unavailable")
}

func (failingTestValuer) String() string {
	return "valuer fallback"
}

type recursiveTestValuer struct{}

func (recursiveTestValuer) Value() (driver.Value, error) {
	return recursiveTestValuer{}, nil
}

func (recursiveTestValuer) String() string {
	return "recursive valuer"
}

type panickingTestValuer struct{}

func (panickingTestValuer) Value() (driver.Value, error) {
	panic("broken valuer")
}

func (panickingTestValuer) String() string {
	return "panicking valuer"
}
