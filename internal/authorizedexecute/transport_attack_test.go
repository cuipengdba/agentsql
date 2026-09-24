package authorizedexecute

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSealedHTTPTransportAttackMatrix(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		secret  string
	}{
		{name: "informational status", secret: "EARLY_HINT_SECRET", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusEarlyHints)
			_, _ = w.Write([]byte("EARLY_HINT_SECRET"))
		}},
		{name: "CRLF response header", secret: "HEADER_SECRET", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Attack", "safe\r\nX-Smuggled: yes")
			_, _ = w.Write([]byte("HEADER_SECRET"))
		}},
		{name: "SSE boundary", secret: "data: SSE_SECRET", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: SSE_SECRET\n\n"))
		}},
		{name: "transfer encoding", secret: "CHUNK_SECRET", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Transfer-Encoding", "chunked")
			_, _ = w.Write([]byte("CHUNK_SECRET"))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := httptest.NewRecorder()
			SealedHTTP(test.handler, 1024).ServeHTTP(destination, httptest.NewRequest(http.MethodPost, "/mcp", nil))
			require.NotContains(t, destination.Body.String(), test.secret)
			require.NotContains(t, destination.Body.String(), "X-Smuggled")
			require.NotEqual(t, "text/event-stream", destination.Header().Get("Content-Type"))
		})
	}
}

func TestSealedHTTPFlushEmitsZeroBytesBeforeSeal(t *testing.T) {
	destination := httptest.NewRecorder()
	handler := SealedHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write([]byte(`{"safe":true}`))
		require.NoError(t, err)
		w.(http.Flusher).Flush()
		require.Empty(t, destination.Body.Bytes(), "flush must emit zero pre-seal bytes")
	}), 1024)
	handler.ServeHTTP(destination, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	require.Equal(t, `{"safe":true}`, destination.Body.String())
}

func TestSealedHTTPFinalizesContentLengthAndOnlyCommitsSanitizedBytes(t *testing.T) {
	destination := httptest.NewRecorder()
	SealedHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "999999")
		_, _ = w.Write([]byte(`{"safe":`))
		_, _ = w.Write([]byte(`true}`))
	}), 1024).ServeHTTP(destination, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	require.Equal(t, `{"safe":true}`, destination.Body.String())
	require.Equal(t, "13", destination.Header().Get("Content-Length"))
}

type byteReader struct{ data []byte }

func (reader *byteReader) Read(target []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	target[0] = reader.data[0]
	reader.data = reader.data[1:]
	return 1, nil
}

type shortWriter struct{ bytes.Buffer }

func (writer *shortWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return writer.Buffer.Write(value[:len(value)-1])
}

func TestLengthPrefixedFrameAttackMatrix(t *testing.T) {
	encode := func(payload []byte) []byte {
		result := make([]byte, 4+len(payload))
		binary.BigEndian.PutUint32(result, uint32(len(payload)))
		copy(result[4:], payload)
		return result
	}

	first, second := encode([]byte("one")), encode([]byte("two"))
	sticky := bytes.NewReader(append(first, second...))
	frame, err := ReadBoundedFrame(sticky, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("one"), frame)
	frame, err = ReadBoundedFrame(sticky, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("two"), frame)

	frame, err = ReadBoundedFrame(&byteReader{data: first}, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("one"), frame)
	_, err = ReadBoundedFrame(bytes.NewReader(first[:len(first)-1]), 3)
	require.Error(t, err, "half frame must fail closed")
	_, err = ReadBoundedFrame(bytes.NewReader([]byte{0, 0, 0, 0}), 3)
	require.Error(t, err, "zero frame is malformed")
	_, err = ReadBoundedFrame(bytes.NewReader([]byte{0, 0, 0, 4}), 3)
	require.Error(t, err, "oversize is rejected from the header")

	partial := &shortWriter{}
	require.ErrorIs(t, WriteSealedFrame(partial, []byte("abc"), 3), io.ErrShortWrite)
	require.Less(t, partial.Len(), 7)
	require.Error(t, WriteSealedFrame(io.Discard, nil, 3))
	require.Error(t, WriteSealedFrame(io.Discard, []byte(strings.Repeat("x", 4)), 3))
}
