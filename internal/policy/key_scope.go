package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var keyScopePattern = regexp.MustCompile(`^db(0|[1-9]|1[0-5]):[A-Za-z0-9][A-Za-z0-9:_-]{2,126}\*?$`)

// ValidateKeyScope accepts an exact Redis/Valkey key or a bounded prefix.
// dbN is the selected logical database, never a caller-controlled SELECT.
func ValidateKeyScope(scope string) error {
	if !keyScopePattern.MatchString(scope) || strings.Count(scope, "*") > 1 {
		return fmt.Errorf("invalid bounded key scope")
	}
	return nil
}

func KeyScopeGranted(allowed, denied []string, database int, key string) bool {
	if database < 0 || database > 15 || !ValidExactKey(key) {
		return false
	}
	target := "db" + strconv.Itoa(database) + ":" + key
	for _, scope := range denied {
		if keyScopeMatches(scope, target) {
			return false
		}
	}
	for _, scope := range allowed {
		if keyScopeMatches(scope, target) {
			return true
		}
	}
	return false
}

func ValidExactKey(key string) bool {
	return ValidateKeyScope("db0:"+key) == nil && !strings.Contains(key, "*")
}

func keyScopeMatches(scope, target string) bool {
	if ValidateKeyScope(scope) != nil {
		return false
	}
	if strings.HasSuffix(scope, "*") {
		return strings.HasPrefix(target, strings.TrimSuffix(scope, "*"))
	}
	return scope == target
}
