package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// NativeReadControl is deliberately metadata-only. Credentials stay in the
// encrypted datasource password fields and must not appear in this JSON.
type NativeReadControl struct {
	Mongo *MongoReadControl         `json:"mongo,omitempty"`
	Redis *RedisSentinelReadControl `json:"redis_sentinel,omitempty"`
}

type RedisSentinelReadControl struct {
	MasterName       string   `json:"master_name"`
	Sentinels        []string `json:"sentinels"`
	AllowedMasters   []string `json:"allowed_masters"`
	SentinelUsername string   `json:"sentinel_username"`
}

// SentinelSecretEnvName binds a gateway-provisioned secret to one tenant and
// datasource. An API administrator cannot select another environment secret.
func SentinelSecretEnvName(tenantID, datasourceID string) string {
	digest := sha256.Sum256([]byte(tenantID + "\x00" + datasourceID))
	return "AGENTSQL_SENTINEL_" + strings.ToUpper(hex.EncodeToString(digest[:12])) + "_PASSWORD"
}

type MongoReadControl struct {
	ReplicaSet string               `json:"replica_set"`
	Members    []string             `json:"members"`
	Indexes    []MongoIndexApproval `json:"indexes"`
}

type MongoIndexApproval struct {
	Collection string   `json:"collection"`
	Name       string   `json:"name"`
	Fields     []string `json:"fields"`
}

func simpleNativeName(value string) bool {
	if len(value) < 1 || len(value) > 120 {
		return false
	}
	for index, char := range value {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func validMongoMember(address string) bool {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" || host != strings.ToLower(host) || strings.ContainsAny(host, "/@?%\\") {
		return false
	}
	for _, char := range host {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '.' || char == '-' {
			continue
		}
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port >= 1 && port <= 65535 && strconv.Itoa(port) == portText
}

// ParseNativeReadControl rejects unknown JSON fields, embedded credentials,
// ambiguous member lists, and unbounded index approvals before persistence or
// connection creation. An empty profile preserves legacy single-node reads.
func ParseNativeReadControl(raw, dbType string) (NativeReadControl, error) {
	if raw == "" {
		raw = "{}"
	}
	if len(raw) > 16384 {
		return NativeReadControl{}, fmt.Errorf("native read control exceeds size limit")
	}
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return NativeReadControl{}, fmt.Errorf("native read control must be an object")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var control NativeReadControl
	if err := decoder.Decode(&control); err != nil {
		return NativeReadControl{}, fmt.Errorf("invalid native read control: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return NativeReadControl{}, fmt.Errorf("native read control has trailing data")
	}
	if control.Redis != nil {
		if control.Mongo != nil || (dbType != "redis" && dbType != "valkey") {
			return NativeReadControl{}, fmt.Errorf("Redis Sentinel control requires a Redis or Valkey datasource")
		}
		redis := control.Redis
		if !simpleNativeName(redis.MasterName) || !simpleNativeName(redis.SentinelUsername) ||
			len(redis.Sentinels) != 3 || len(redis.AllowedMasters) < 2 || len(redis.AllowedMasters) > 6 {
			return NativeReadControl{}, fmt.Errorf("Redis Sentinel topology or identity is invalid")
		}
		addresses := map[string]bool{}
		for _, address := range append(append([]string{}, redis.Sentinels...), redis.AllowedMasters...) {
			if !validMongoMember(address) || addresses[address] {
				return NativeReadControl{}, fmt.Errorf("Redis Sentinel address allowlist is invalid")
			}
			addresses[address] = true
		}
		return control, nil
	}
	if control.Mongo == nil {
		return control, nil
	}
	if dbType != "mongodb" {
		return NativeReadControl{}, fmt.Errorf("MongoDB read control requires a MongoDB datasource")
	}
	mongo := control.Mongo
	if !simpleNativeName(mongo.ReplicaSet) || len(mongo.Members) != 3 ||
		len(mongo.Indexes) < 1 || len(mongo.Indexes) > 16 {
		return NativeReadControl{}, fmt.Errorf("MongoDB replica set and index approvals are required")
	}
	uniqueMembers := map[string]bool{}
	for _, member := range mongo.Members {
		if !validMongoMember(member) || uniqueMembers[member] {
			return NativeReadControl{}, fmt.Errorf("MongoDB member allowlist is invalid")
		}
		uniqueMembers[member] = true
	}
	uniqueIndexes := map[string]bool{}
	for _, index := range mongo.Indexes {
		if !simpleNativeName(index.Collection) || !simpleNativeName(index.Name) ||
			len(index.Fields) < 1 || len(index.Fields) > 4 || uniqueIndexes[index.Collection+"\x00"+index.Name] {
			return NativeReadControl{}, fmt.Errorf("MongoDB index approval is invalid")
		}
		uniqueIndexes[index.Collection+"\x00"+index.Name] = true
		fields := map[string]bool{}
		for _, field := range index.Fields {
			if !simpleNativeName(field) || fields[field] {
				return NativeReadControl{}, fmt.Errorf("MongoDB index fields are invalid")
			}
			fields[field] = true
		}
	}
	return control, nil
}
