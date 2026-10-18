package businessdb

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDMDeadlineConnClosesBlockedSocket(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	connection := &dmDeadlineConn{Conn: client, timeout: 30 * time.Millisecond}
	finished := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, err := server.Read(buffer)
		if err == nil {
			_, err = server.Read(buffer)
		}
		finished <- err
	}()
	_, err := connection.Write([]byte{'x'})
	require.NoError(t, err)
	_, err = connection.Read(make([]byte, 1))
	require.True(t, errors.Is(err, os.ErrDeadlineExceeded), "error=%v", err)
	select {
	case serverErr := <-finished:
		require.Error(t, serverErr, "the remote endpoint must see the closed socket")
	case <-time.After(time.Second):
		t.Fatal("the remote endpoint did not observe the closed socket")
	}
}
