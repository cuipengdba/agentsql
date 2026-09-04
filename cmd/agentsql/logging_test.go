package main

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewLoggerFromEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		format    string
		wantLevel zerolog.Level
		wantError bool
	}{
		{name: "defaults", wantLevel: zerolog.InfoLevel},
		{name: "JSON debug", level: "debug", format: "json", wantLevel: zerolog.DebugLevel},
		{name: "console warn", level: "warn", format: "console", wantLevel: zerolog.WarnLevel},
		{name: "invalid level", level: "verbose", wantError: true},
		{name: "invalid format", format: "text", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", test.level)
			t.Setenv("LOG_FORMAT", test.format)

			logger, err := newLogger(new(bytes.Buffer))
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantLevel, logger.GetLevel())
		})
	}
}
