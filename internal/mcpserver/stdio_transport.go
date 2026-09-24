package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"

	authorizedexecute "github.com/cuipengdba/agentsql/internal/authorizedexecute"
)

type sealedLineReader struct {
	reader   *bufio.Reader
	closer   io.Closer
	limit    int
	pending  []byte
	terminal error
}

func newSealedLineReader(reader io.ReadCloser, limit int) *sealedLineReader {
	if limit <= 0 || limit > authorizedexecute.DefaultLimits.EnvelopeBytes {
		limit = authorizedexecute.DefaultLimits.EnvelopeBytes
	}
	return &sealedLineReader{reader: bufio.NewReaderSize(reader, min(limit, 64<<10)), closer: reader, limit: limit}
}

func (reader *sealedLineReader) Read(destination []byte) (int, error) {
	if len(reader.pending) == 0 && reader.terminal == nil {
		var frame []byte
		for {
			fragment, err := reader.reader.ReadSlice('\n')
			if len(fragment) > reader.limit-len(frame) {
				reader.terminal = &authorizedexecute.AuthError{Reason: authorizedexecute.ReasonEnvelopeLimit}
				break
			}
			frame = append(frame, fragment...)
			if err == nil {
				break
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(err, io.EOF) && len(frame) > 0 {
				reader.terminal = &authorizedexecute.AuthError{Reason: authorizedexecute.ReasonFrameLimit}
				break
			}
			reader.terminal = err
			break
		}
		if reader.terminal == nil {
			payload := frame[:len(frame)-1]
			if len(payload) == 0 || bytes.ContainsAny(payload, "\r\n") || !json.Valid(payload) {
				reader.terminal = &authorizedexecute.AuthError{Reason: authorizedexecute.ReasonDatabaseFailure}
			} else {
				reader.pending = frame
			}
		}
	}
	if len(reader.pending) > 0 {
		count := copy(destination, reader.pending)
		reader.pending = reader.pending[count:]
		return count, nil
	}
	if reader.terminal != nil {
		err := reader.terminal
		reader.terminal = io.EOF
		return 0, err
	}
	return 0, io.EOF
}

func (reader *sealedLineReader) Close() error {
	if reader.closer == nil {
		return nil
	}
	return reader.closer.Close()
}

type sealedLineWriter struct {
	mu     sync.Mutex
	writer io.Writer
	closer io.Closer
	limit  int
}

func newSealedLineWriter(writer io.Writer, limit int) *sealedLineWriter {
	if limit <= 0 || limit > authorizedexecute.DefaultLimits.EnvelopeBytes {
		limit = authorizedexecute.DefaultLimits.EnvelopeBytes
	}
	return &sealedLineWriter{writer: writer, limit: limit}
}

func (writer *sealedLineWriter) Write(frame []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(frame) == 0 || len(frame) > writer.limit || frame[len(frame)-1] != '\n' {
		return 0, &authorizedexecute.AuthError{Reason: authorizedexecute.ReasonEnvelopeLimit}
	}
	payload := frame[:len(frame)-1]
	if len(payload) == 0 || bytes.ContainsAny(payload, "\r\n") || !json.Valid(payload) {
		return 0, &authorizedexecute.AuthError{Reason: authorizedexecute.ReasonEnvelopeLimit}
	}
	sealed := append([]byte(nil), frame...)
	written, err := writer.writer.Write(sealed)
	if err == nil && written != len(sealed) {
		err = io.ErrShortWrite
	}
	return written, err
}

func (writer *sealedLineWriter) Close() error {
	if writer.closer == nil {
		return nil
	}
	return writer.closer.Close()
}
