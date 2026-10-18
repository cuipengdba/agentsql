package businessdb

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	dm8 "github.com/godoes/gorm-dameng/dm8"
)

// The DM driver closes a canceled connection only after a synchronous
// rollback. A server-side lock wait can keep that rollback blocked. Bound each
// wire request as well and close the socket on expiry, so the server releases
// the waiting session even when the driver's cancellation path stalls.
func registerDMBoundedDial(timeoutMS int) string {
	name := "agentsql-dm-" + strconv.Itoa(timeoutMS)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	dm8.RegisterDialContext(name, func(ctx context.Context, address string) (net.Conn, error) {
		connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		bounded := &dmDeadlineConn{Conn: connection, timeout: timeout, done: make(chan struct{})}
		if ctx.Done() != nil {
			go func() {
				select {
				case <-ctx.Done():
					_ = bounded.Close()
				case <-bounded.done:
				}
			}()
		}
		return bounded, nil
	})
	return name
}

type dmDeadlineConn struct {
	net.Conn
	timeout   time.Duration
	done      chan struct{}
	closeOnce sync.Once
}

func (connection *dmDeadlineConn) Close() error {
	var closeErr error
	connection.closeOnce.Do(func() {
		if connection.done != nil {
			close(connection.done)
		}
		closeErr = connection.Conn.Close()
	})
	return closeErr
}

func (connection *dmDeadlineConn) Write(data []byte) (int, error) {
	if err := connection.Conn.SetDeadline(time.Now().Add(connection.timeout)); err != nil {
		_ = connection.Close()
		return 0, err
	}
	written, err := connection.Conn.Write(data)
	if err != nil {
		_ = connection.Close()
	}
	return written, err
}

func (connection *dmDeadlineConn) Read(data []byte) (int, error) {
	read, err := connection.Conn.Read(data)
	if err != nil {
		_ = connection.Close()
	}
	return read, err
}
