package mcpserver

import (
	"bytes"
	"io"
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

func TestSealedStdioReaderRejectsOversizeBeforeSDKObservesBytes(t *testing.T) {
	reader := newSealedLineReader(readCloser{bytes.NewBufferString(`{"value":"0123456789"}` + "\n")}, 8)
	buffer := make([]byte, 32)
	count, err := reader.Read(buffer)
	require.Zero(t, count)
	var authorizationError *authorizedexecute.AuthError
	require.ErrorAs(t, err, &authorizationError)
	require.Equal(t, authorizedexecute.ReasonEnvelopeLimit, authorizationError.Reason)
}
