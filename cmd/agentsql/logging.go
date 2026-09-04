package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

var errInvalidLogFormat = errors.New("LOG_FORMAT must be json or console")

func newLogger(output io.Writer) (zerolog.Logger, error) {
	levelName := strings.TrimSpace(os.Getenv("LOG_LEVEL"))
	if levelName == "" {
		levelName = zerolog.InfoLevel.String()
	}
	level, err := zerolog.ParseLevel(strings.ToLower(levelName))
	if err != nil {
		return zerolog.Nop(), fmt.Errorf("parse LOG_LEVEL %q: %w", levelName, err)
	}

	logOutput := output
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	switch format {
	case "", "json":
	case "console":
		logOutput = zerolog.ConsoleWriter{Out: output, TimeFormat: time.RFC3339}
	default:
		return zerolog.Nop(), fmt.Errorf("configure LOG_FORMAT %q: %w", format, errInvalidLogFormat)
	}

	return zerolog.New(logOutput).Level(level).With().Timestamp().Logger(), nil
}
