package adminapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"sync"
	"time"
)

const (
	loginWindow               = time.Minute
	loginMaxAttempts          = 10
	loginMaxBuckets           = 8192
	sharedLoginAttemptTimeout = 5 * time.Second
)

type loginAttemptBucket struct {
	started time.Time
	count   int
}

// loginAttemptLimiter bounds password attempts per tenant/account, including
// unknown accounts, without relying on untrusted proxy forwarding headers.
// This is used only for SQLite single-instance deployments. PostgreSQL uses
// the shared metadata repository instead.
type loginAttemptLimiter struct {
	mu      sync.Mutex
	buckets map[[32]byte]loginAttemptBucket
	now     func() time.Time
}

func (handler *Handler) allowLoginAttempt(ctx context.Context, tenant, username string) (bool, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(username) == "" {
		return false, nil
	}
	shared := handler.deps.Runtime.Store.SharedLoginAttempts()
	if shared == nil {
		return handler.loginAttempts.allow(tenant, username), nil
	}
	attemptCtx, cancel := context.WithTimeout(ctx, sharedLoginAttemptTimeout)
	defer cancel()
	return shared.Allow(attemptCtx, loginAttemptDigest(handler.tokenKey, tenant, username))
}

func loginAttemptDigest(secret []byte, tenant, username string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(tenant))))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.ToLower(strings.TrimSpace(username))))
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
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
