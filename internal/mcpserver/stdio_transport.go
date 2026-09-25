package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sync"

	authorizedexecute "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const legacyMCPProtocolVersion = "2025-06-18"

// legacyProtocolTransport prevents the SDK from silently negotiating unknown
// legacy clients to a newer version. v0.4 has exactly one reviewed protocol.
type legacyProtocolTransport struct{ mcp.Transport }

func (legacyProtocolTransport) SupportsProtocolVersion(version string) bool {
	return version == legacyMCPProtocolVersion
}

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
		if _, err := reader.reader.Peek(1); err != nil {
			reader.terminal = err
		}
		var frame []byte
		var err error
		if reader.terminal == nil {
			frame, err = authorizedexecute.ReadBoundedFrame(reader.reader, reader.limit)
		}
		if reader.terminal == nil && err != nil {
			reader.terminal = err
		} else if reader.terminal == nil {
			if bytes.ContainsAny(frame, "\r\n") || !json.Valid(frame) {
				reader.terminal = &authorizedexecute.AuthError{Reason: authorizedexecute.ReasonDatabaseFailure}
			} else {
				// The SDK's IO transport is newline-oriented. The newline exists
				// only inside this adapter and never crosses the stdio wire.
				reader.pending = append(frame, '\n')
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
	sealed := append([]byte(nil), payload...)
	if err := authorizedexecute.WriteSealedFrame(writer.writer, sealed, writer.limit); err != nil {
		return 0, err
	}
	// Report the bytes consumed from the SDK, not the wire prefix.
	return len(frame), nil
}

func (writer *sealedLineWriter) Close() error {
	if writer.closer == nil {
		return nil
	}
	return writer.closer.Close()
}

var _ mcp.ProtocolVersionSupporter = legacyProtocolTransport{}
var _ mcp.Transport = legacyProtocolTransport{}
