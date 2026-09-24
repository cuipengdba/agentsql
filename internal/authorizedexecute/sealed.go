package authorizedexecute

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
)

var ErrUnsealed = errors.New("authorized response is not sealed")

// SealedResponseWriter buffers headers, status and body privately. Flush is a
// no-op, so middleware and handlers cannot emit progress or keepalive bytes.
type SealedResponseWriter struct {
	header      http.Header
	status      int
	body        bytes.Buffer
	limit       int
	sealed      bool
	failed      error
	afterCommit []func()
}

func NewSealedResponseWriter(limit int) *SealedResponseWriter {
	if limit <= 0 || limit > DefaultLimits.EnvelopeBytes {
		limit = DefaultLimits.EnvelopeBytes
	}
	return &SealedResponseWriter{header: make(http.Header), limit: limit}
}

func (writer *SealedResponseWriter) Header() http.Header { return writer.header }
func (writer *SealedResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}
func (writer *SealedResponseWriter) Write(body []byte) (int, error) {
	if writer.failed != nil {
		return 0, writer.failed
	}
	if writer.sealed {
		writer.failed = ErrUnsealed
		return 0, writer.failed
	}
	if len(body) > writer.limit-writer.body.Len() {
		writer.failed = limitError(ReasonEnvelopeLimit)
		return 0, writer.failed
	}
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.body.Write(body)
}
func (*SealedResponseWriter) Flush() {}

// AfterCommit registers a side effect that is safe only after the complete
// sealed body has reached the underlying writer.
func (writer *SealedResponseWriter) AfterCommit(callback func()) {
	if writer != nil && callback != nil && !writer.sealed {
		writer.afterCommit = append(writer.afterCommit, callback)
	}
}

func (writer *SealedResponseWriter) Seal() error {
	if writer == nil || writer.failed != nil {
		if writer == nil {
			return ErrUnsealed
		}
		return writer.failed
	}
	writer.sealed = true
	return nil
}

func (writer *SealedResponseWriter) CommitTo(destination http.ResponseWriter) error {
	if writer == nil || !writer.sealed || writer.failed != nil {
		return ErrUnsealed
	}
	for key, values := range writer.header {
		for _, value := range values {
			destination.Header().Add(key, value)
		}
	}
	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}
	destination.WriteHeader(status)
	written, err := destination.Write(writer.body.Bytes())
	if err != nil {
		return err
	}
	if written != writer.body.Len() {
		return io.ErrShortWrite
	}
	for _, callback := range writer.afterCommit {
		callback()
	}
	return nil
}

// SealedHTTP buffers one handler invocation and commits only after it returns
// successfully and the complete bounded body has been sealed.
func SealedHTTP(next http.Handler, limit int) http.Handler {
	return http.HandlerFunc(func(destination http.ResponseWriter, request *http.Request) {
		buffer := NewSealedResponseWriter(limit)
		next.ServeHTTP(buffer, request)
		if buffer.failed != nil {
			http.Error(destination, string(StableError(buffer.failed).Reason), http.StatusRequestEntityTooLarge)
			return
		}
		if err := buffer.Seal(); err != nil {
			http.Error(destination, string(ReasonDatabaseFailure), http.StatusInternalServerError)
			return
		}
		_ = buffer.CommitTo(destination)
	})
}

// ReadBoundedFrame validates the attacker-controlled length before allocation.
func ReadBoundedFrame(reader io.Reader, limit int) ([]byte, error) {
	if limit <= 0 || limit > DefaultLimits.FrameBytes {
		limit = DefaultLimits.FrameBytes
	}
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, limitError(ReasonFrameLimit)
	}
	length := binary.BigEndian.Uint32(header[:])
	if uint64(length) > uint64(limit) {
		return nil, limitError(ReasonFrameLimit)
	}
	frame := make([]byte, int(length))
	if _, err := io.ReadFull(reader, frame); err != nil {
		return nil, limitError(ReasonFrameLimit)
	}
	return frame, nil
}

// WriteSealedFrame caps a complete frame before writing its header or payload.
func WriteSealedFrame(writer io.Writer, frame []byte, limit int) error {
	if limit <= 0 || limit > DefaultLimits.FrameBytes {
		limit = DefaultLimits.FrameBytes
	}
	if len(frame) > limit {
		return limitError(ReasonFrameLimit)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	buffer := make([]byte, 4+len(frame))
	copy(buffer, header[:])
	copy(buffer[4:], frame)
	_, err := writer.Write(buffer)
	return err
}
