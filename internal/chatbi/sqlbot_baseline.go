package chatbi

const (
	sqlbotHistoryCommit = "aa41fc1033bb974a703df9200a98ec8b9eec5f25"
	sqlbotV1103Commit   = "96f06c7eade60ecf3e6af0313821373761d1fb85"
)

// Accept only snapshot metadata from pinned Community source revisions.
// This identifies the expected export schema; it does not authenticate the
// snapshot file or establish complete application migration.
func pinnedSQLBotSnapshot(version, commit string) bool {
	switch version {
	case "v1.10.2":
		return commit == sqlbotHistoryCommit
	case "v1.10.3":
		return commit == sqlbotV1103Commit
	default:
		return false
	}
}
