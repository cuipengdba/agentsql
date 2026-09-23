package model

import "time"

const (
	RedactionKeyStateStandby = "standby"
	RedactionKeyStateActive  = "active"
	RedactionKeyStateLegacy  = "legacy"
	RedactionKeyStateRetired = "retired"
)

// RedactionKeyVersion is a key-version registry record. It never contains key material.
type RedactionKeyVersion struct {
	ID             string
	State          string
	Commitment     string
	Label          string
	ConfigRevision string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ActivatedAt    *time.Time
	RetiredAt      *time.Time
}

// ManagementAuditOutbox is one metadata-side management audit delivery.
type ManagementAuditOutbox struct {
	EventUUID     string
	Action        string
	ActorType     string
	ActorID       string
	DetailsJSON   string
	CreatedAt     time.Time
	Attempts      int
	ClaimedBy     string
	ClaimedAt     *time.Time
	LastError     string
	NextAttemptAt time.Time
	DeliveredAt   *time.Time
}
