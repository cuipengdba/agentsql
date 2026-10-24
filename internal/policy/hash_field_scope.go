package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cuipengdba/agentsql/internal/model"
)

var hashFieldNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// ParseHashFieldScope accepts one exact key and one simple Hash field. The #
// separator cannot occur in a controlled key, so the policy identity is unique.
func ParseHashFieldScope(scope string) (string, string, error) {
	keyScope, field, found := strings.Cut(scope, "#")
	if !found || strings.Contains(field, "#") || !hashFieldNamePattern.MatchString(field) ||
		ValidateKeyScope(keyScope) != nil || strings.HasSuffix(keyScope, "*") {
		return "", "", fmt.Errorf("invalid exact Hash field scope")
	}
	return keyScope, field, nil
}

// HashFieldsGranted requires an exact key grant as well as an explicit grant
// for every requested field. Prefix grants alone never authorize Hash values.
func HashFieldsGranted(decision *model.PolicyDecision, database int, key string, fields []string) bool {
	if decision == nil || database < 0 || database > 15 || !ValidExactKey(key) ||
		len(fields) < 1 || len(fields) > 16 {
		return false
	}
	keyScope := "db" + strconv.Itoa(database) + ":" + key
	exactKeyGranted := false
	for _, scope := range decision.AllowedKeys {
		if scope == keyScope {
			exactKeyGranted = true
			break
		}
	}
	if !exactKeyGranted || !KeyScopeGranted(decision.AllowedKeys, decision.DeniedKeys, database, key) {
		return false
	}
	allowed := decision.HashFieldACL[keyScope]
	for _, field := range fields {
		if !hashFieldNamePattern.MatchString(field) {
			return false
		}
		found := false
		for _, grant := range allowed {
			if grant == field {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
