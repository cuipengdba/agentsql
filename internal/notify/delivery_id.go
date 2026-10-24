package notify

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// DeliveryID is a stable, non-secret idempotency key for one tenant, channel,
// and committed audit-stream sequence. Receivers must deduplicate this key;
// network delivery itself is at least once.
func DeliveryID(tenantID, channelID string, sequence int64) (string, error) {
	if tenantID == "" || channelID == "" || sequence < 1 || len(tenantID) > 65535 || len(channelID) > 65535 {
		return "", errors.New("invalid notification delivery identity")
	}
	message := make([]byte, 0, 48+len(tenantID)+len(channelID))
	message = append(message, "agentsql-notification-delivery/v1"...)
	message = append(message, 0)
	message = binary.BigEndian.AppendUint16(message, uint16(len(tenantID)))
	message = append(message, tenantID...)
	message = binary.BigEndian.AppendUint16(message, uint16(len(channelID)))
	message = append(message, channelID...)
	message = binary.BigEndian.AppendUint64(message, uint64(sequence))
	digest := sha256.Sum256(message)
	return hex.EncodeToString(digest[:]), nil
}
