package bootstrap

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strconv"

	"github.com/cuipengdba/agentsql/internal/rbac"
	"github.com/cuipengdba/agentsql/internal/store"
)

var managedFileStageToken = regexp.MustCompile(`^agentsql-file-[1-9][0-9]{0,18}\.(?:csv|xlsx)$`)

// DeriveSQLBotImportIdentity lets the gateway reproduce a child connector
// after an HTTP response is lost. The random staged-file ID is scoped to the
// authenticated parent and user, and the server secret never leaves the
// gateway. Distinct HMAC domains separate the public name from the token.
func (runtime *Runtime) DeriveSQLBotImportIdentity(parent store.SQLBotBinding, principal rbac.Principal, filePath string) (string, string, error) {
	if runtime == nil || len(runtime.secret) != 32 || !managedFileStageToken.MatchString(filePath) ||
		parent.ID == "" || parent.Revision < 1 || parent.ParentBindingID != "" || !parent.Enabled ||
		parent.TenantID == "" || parent.TenantID != principal.TenantID ||
		parent.AgentSQLUserID == "" || parent.AgentSQLUserID != principal.UserID ||
		parent.SchemaName != "agentsql_files" {
		return "", "", ErrManagedFileStage
	}
	material := []byte(parent.TenantID + "\x00" + parent.ID + "\x00" + strconv.FormatInt(parent.Revision, 10) + "\x00" + principal.UserID + "\x00" + filePath)
	nameMAC := hmac.New(sha256.New, runtime.secret)
	nameMAC.Write([]byte("agentsql:sqlbot-import-name:v1\x00"))
	nameMAC.Write(material)
	name := "agentsql-import-" + hex.EncodeToString(nameMAC.Sum(nil)[:16])
	tokenMAC := hmac.New(sha256.New, runtime.secret)
	tokenMAC.Write([]byte("agentsql:sqlbot-import-token:v1\x00"))
	tokenMAC.Write(material)
	return name, base64.RawURLEncoding.EncodeToString(tokenMAC.Sum(nil)), nil
}
