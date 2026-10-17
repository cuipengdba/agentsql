package adminapi

import (
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

const (
	loginWindow      = time.Minute
	loginMaxAttempts = 10
	loginMaxBuckets  = 8192
)

type loginAttemptBucket struct {
	started time.Time
	count   int
}

// loginAttemptLimiter bounds password attempts per tenant/account, including
// unknown accounts, without relying on untrusted proxy forwarding headers.
// This is local to one instance; cross-instance admission is a v1.4.0 gate.
type loginAttemptLimiter struct {
	mu      sync.Mutex
	buckets map[[32]byte]loginAttemptBucket
	now     func() time.Time
}

func newLoginAttemptLimiter() *loginAttemptLimiter {
	return &loginAttemptLimiter{buckets: make(map[[32]byte]loginAttemptBucket), now: time.Now}
}

func (limiter *loginAttemptLimiter) allow(tenant, username string) bool {
	if limiter == nil || strings.TrimSpace(username) == "" || strings.TrimSpace(tenant) == "" {
		return false
	}
	key := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(tenant)) + "\x00" + strings.ToLower(strings.TrimSpace(username))))
	now := limiter.now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	entry, exists := limiter.buckets[key]
	if exists && now.Sub(entry.started) >= loginWindow {
		delete(limiter.buckets, key)
		exists = false
	}
	if !exists {
		if len(limiter.buckets) >= loginMaxBuckets {
			for candidate, bucket := range limiter.buckets {
				if now.Sub(bucket.started) >= loginWindow {
					delete(limiter.buckets, candidate)
				}
			}
			if len(limiter.buckets) >= loginMaxBuckets {
				return false
			}
		}
		entry = loginAttemptBucket{started: now}
	}
	if entry.count >= loginMaxAttempts {
		return false
	}
	entry.count++
	limiter.buckets[key] = entry
	return true
}
