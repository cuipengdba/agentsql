package mcpserver

import (
	"bytes"
	"io"
	"strings"
	"testing"

	authorizedexecute "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/stretchr/testify/require"
)

type readCloser struct{ io.Reader }

func (readCloser) Close() error { return nil }

func TestSealedStdioWriterWritesOnlyCompleteBoundedJSONFrame(t *testing.T) {
	var destination bytes.Buffer
	writer := newSealedLineWriter(&destination, 32)
	written, err := writer.Write([]byte(`{"jsonrpc":"2.0"}`))
	require.Error(t, err)
	require.Zero(t, written)
	require.Empty(t, destination.Bytes())
	written, err = writer.Write([]byte("{\"jsonrpc\":\"2.0\"}\n"))
	require.NoError(t, err)
	require.Equal(t, destination.Len(), written)
}

type fragmentedReadCloser struct {
	data []byte
}

func (reader *fragmentedReadCloser) Read(target []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	target[0] = reader.data[0]
	reader.data = reader.data[1:]
	return 1, nil
}
func (*fragmentedReadCloser) Close() error { return nil }

func TestSealedStdioFramingAttackMatrix(t *testing.T) {
	valid := []byte("{\"jsonrpc\":\"2.0\",\"id\":1}\n")
	reader := newSealedLineReader(&fragmentedReadCloser{data: append([]byte(nil), valid...)}, len(valid))
	observed, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, valid, observed, "fragmentation must not change the sealed frame")

	attacks := []struct {
		name  string
		frame string
		limit int
	}{
		{name: "half frame", frame: `{"jsonrpc":"2.0"}`, limit: 64},
		{name: "CRLF injection", frame: "{\"jsonrpc\":\"2.0\"}\r\n", limit: 64},
		{name: "embedded newline smuggling", frame: "{\"jsonrpc\":\n\"2.0\"}\n{\"smuggled\":true}\n", limit: 128},
		{name: "malformed then valid", frame: "{bad}\n{\"jsonrpc\":\"2.0\"}\n", limit: 128},
		{name: "oversize", frame: strings.Repeat("x", 65) + "\n", limit: 64},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			input := newSealedLineReader(readCloser{strings.NewReader(attack.frame)}, attack.limit)
			buffer := make([]byte, 256)
			count, err := input.Read(buffer)
			require.Error(t, err)
			require.Zero(t, count, "SDK must observe zero attacker bytes")
		})
	}

	writes := []string{
		`{"jsonrpc":"2.0"}`,
		"{\"jsonrpc\":\"2.0\"}\r\n",
		"{\"jsonrpc\":\n\"2.0\"}\n",
		"{bad}\n",
		strings.Repeat("x", 65) + "\n",
	}
	for index, frame := range writes {
		t.Run("write attack "+string(rune('a'+index)), func(t *testing.T) {
			var destination bytes.Buffer
			writer := newSealedLineWriter(&destination, 64)
			written, err := writer.Write([]byte(frame))
			require.Error(t, err)
			require.Zero(t, written)
			require.Empty(t, destination.Bytes())
		})
	}
}

func TestSealedStdioReaderRejectsOversizeBeforeSDKObservesBytes(t *testing.T) {
	reader := newSealedLineReader(readCloser{bytes.NewBufferString(`{"value":"0123456789"}` + "\n")}, 8)
	buffer := make([]byte, 32)
	count, err := reader.Read(buffer)
	require.Zero(t, count)
	var authorizationError *authorizedexecute.AuthError
	require.ErrorAs(t, err, &authorizationError)
	require.Equal(t, authorizedexecute.ReasonEnvelopeLimit, authorizationError.Reason)
}
