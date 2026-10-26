package managedfile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidOperation = errors.New("invalid managed file operation")

type OperationID [16]byte

func NewOperationID() (OperationID, error) {
	var id OperationID
	_, err := rand.Read(id[:])
	return id, err
}

func ParseOperationID(value string) (OperationID, error) {
	var id OperationID
	if len(value) != 32 || strings.ToLower(value) != value {
		return id, ErrInvalidOperation
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != len(id) {
		return id, ErrInvalidOperation
	}
	copy(id[:], raw)
	if id == (OperationID{}) {
		return id, ErrInvalidOperation
	}
	return id, nil
}

// TableName is stable across restarts and cannot contain client text.
func TableName(id OperationID, sheet int) (string, error) {
	if id == (OperationID{}) || sheet < 0 || sheet >= 8 {
		return "", ErrInvalidOperation
	}
	seed := append(id[:], byte(sheet))
	digest := sha256.Sum256(seed)
	return "agentsql_file_" + hex.EncodeToString(digest[:16]), nil
}

// PolicyID ties a generated table to exactly one control-plane column policy.
func PolicyID(id OperationID, sheet int) (string, error) {
	if id == (OperationID{}) || sheet < 0 || sheet >= 8 {
		return "", ErrInvalidOperation
	}
	return "agentsql-managed-file-" + hex.EncodeToString(id[:]) + "-" + strconv.Itoa(sheet), nil
}
