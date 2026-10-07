package businessdb

import (
	"fmt"
	"strings"
)

// ClassifyNativeCommand is an additional HTTP boundary. The adapter still
// validates the command, arguments and native statement before execution.
// "write" is never executable through the admin preview.
func ClassifyNativeCommand(dbType string, request NativeQueryRequest) (string, error) {
	command, _, err := normalizedNativeCommand(request)
	if err != nil {
		return "", err
	}
	read, write := "", ""
	switch dbType {
	case "redis", "valkey":
		if redisReadCommands[command] {
			return "read", nil
		}
		if redisWriteCommands[command] {
			return "write", nil
		}
	case "memcached":
		if memcachedReadCommands[command] {
			return "read", nil
		}
		if memcachedWriteCommands[command] {
			return "write", nil
		}
	case "mongodb":
		if mongoReads[command] {
			return "read", nil
		}
		if mongoWrites[command] {
			return "write", nil
		}
	case "couchdb":
		if err := couchDBCommandAllowed(command, true); err == nil {
			return "read", nil
		}
		if err := couchDBCommandAllowed(command, false); err == nil {
			return "write", nil
		}
	case "cassandra", "scylla":
		if err := cassandraCommandAllowed(command, true); err == nil {
			return "read", nil
		}
		if err := cassandraCommandAllowed(command, false); err == nil {
			return "write", nil
		}
	case "hbase":
		if err := hbaseCommandAllowed(command, true); err == nil {
			return "read", nil
		}
		if err := hbaseCommandAllowed(command, false); err == nil {
			return "write", nil
		}
	case "milvus":
		if err := milvusCommandAllowed(command, true); err == nil {
			return "read", nil
		}
		if err := milvusCommandAllowed(command, false); err == nil {
			return "write", nil
		}
	case "qdrant":
		if err := qdrantCommandAllowed(command, true); err == nil {
			return "read", nil
		}
		if err := qdrantCommandAllowed(command, false); err == nil {
			return "write", nil
		}
	case "weaviate":
		if err := weaviateCommandAllowed(command, true); err == nil {
			return "read", nil
		}
		if err := weaviateCommandAllowed(command, false); err == nil {
			return "write", nil
		}
	case "janusgraph":
		if command != "G" || len(request.Args) != 1 {
			break
		}
		if err := gremlinAllowed(request.Args[0], true); err == nil {
			return "read", nil
		}
		if err := gremlinAllowed(request.Args[0], false); err == nil {
			return "write", nil
		}
	case "couchbase":
		read, write = "SELECT", "INSERT UPSERT UPDATE DELETE"
	case "neo4j":
		read, write = "MATCH RETURN UNWIND SHOW CALL", "CREATE MERGE DROP SET REMOVE DELETE"
	case "nebula":
		read, write = "SHOW DESCRIBE MATCH GO LOOKUP GET FETCH", "INSERT UPDATE UPSERT DELETE"
	case "influxdb":
		read, write = "SHOW SELECT EXPLAIN FLUX FROM", "WRITE DELETE"
	case "prometheus":
		read = "QUERY QUERY_RANGE SERIES LABELS LABEL_VALUES"
	case "timescaledb":
		read, write = "SELECT SHOW EXPLAIN WITH", "INSERT UPDATE DELETE CREATE DROP ALTER"
	case "clickhouse":
		read, write = "SELECT SHOW DESCRIBE EXISTS", "INSERT ALTER OPTIMIZE"
	case "doris", "starrocks":
		read, write = "SELECT SHOW DESCRIBE EXPLAIN", "INSERT UPDATE DELETE ALTER"
	}
	if containsNativeCommand(read, command) {
		return "read", nil
	}
	if containsNativeCommand(write, command) {
		return "write", nil
	}
	return "", fmt.Errorf("native command is not allowed")
}

func containsNativeCommand(list, command string) bool {
	for _, item := range strings.Fields(list) {
		if item == command {
			return true
		}
	}
	return false
}
