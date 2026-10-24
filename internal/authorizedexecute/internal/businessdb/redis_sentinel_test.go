package businessdb

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

func TestSentinelDialerRejectsUnknownBeforeConnection(t *testing.T) {
	control := &model.RedisSentinelReadControl{
		Sentinels:      []string{"s1.test:26379", "s2.test:26379", "s3.test:26379"},
		AllowedMasters: []string{"r1.test:6379", "r2.test:6379"},
	}
	dial := sentinelAllowedDialer(control, &tls.Config{MinVersion: tls.VersionTLS12}, 100*time.Millisecond)
	if _, err := dial(context.Background(), "tcp", "evil.test:6379"); err == nil {
		t.Fatal("unknown master was dialed")
	}
	if _, err := dial(context.Background(), "unix", "r1.test:6379"); err == nil {
		t.Fatal("non-TCP Sentinel address was accepted")
	}
}
