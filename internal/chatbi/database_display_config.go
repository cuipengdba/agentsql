package chatbi

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// SQLBot's unchanged edit-tables form validates host, port, database and
// PostgreSQL schema even though this operation needs no direct connection.
// Its public AES wire format is only a browser compatibility envelope. These
// conspicuous sentinel values are never used to connect and disclose no
// server-owned address, username, password, token or gateway configuration.
func databaseDisplayConfiguration() (string, error) {
	plain, err := json.Marshal(map[string]any{
		"host": "AGENTSQL_GATEWAY_MANAGED", "port": 1,
		"username": "AGENTSQL_GATEWAY_MANAGED", "password": "",
		"database": "AGENTSQL_GATEWAY_MANAGED", "dbSchema": "AGENTSQL_GATEWAY_MANAGED",
		"extraJdbc": "", "timeout": 30,
	})
	if err != nil {
		return "", ErrUnavailable
	}
	block, err := aes.NewCipher([]byte("SQLBot1234567890"))
	if err != nil {
		return "", ErrUnavailable
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	for range padding {
		plain = append(plain, byte(padding))
	}
	encoded := make([]byte, len(plain))
	for offset := 0; offset < len(plain); offset += aes.BlockSize {
		block.Encrypt(encoded[offset:offset+aes.BlockSize], plain[offset:offset+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

// The original edit form encrypts its visible fields with SQLBot's public
// wire key. Accept only the gateway-managed sentinel when probing an existing
// binding; this browser envelope must never become a connection target.
func validDatabaseMetadataConfiguration(encoded string) bool {
	if encoded == "" || len(encoded) > 65536 {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 49152 || len(data)%aes.BlockSize != 0 {
		return false
	}
	block, err := aes.NewCipher([]byte("SQLBot1234567890"))
	if err != nil {
		return false
	}
	plain := make([]byte, len(data))
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], data[offset:offset+aes.BlockSize])
	}
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize {
		return false
	}
	for _, value := range plain[len(plain)-padding:] {
		if int(value) != padding {
			return false
		}
	}
	plain = plain[:len(plain)-padding]
	var config struct {
		Host       string            `json:"host"`
		Port       int               `json:"port"`
		Username   string            `json:"username"`
		Password   string            `json:"password"`
		Database   string            `json:"database"`
		DBSchema   string            `json:"dbSchema"`
		ExtraJDBC  string            `json:"extraJdbc"`
		Filename   string            `json:"filename"`
		Sheets     []json.RawMessage `json:"sheets"`
		Mode       *string           `json:"mode"`
		Timeout    *int              `json:"timeout"`
		LowVersion *bool             `json:"lowVersion"`
		SSL        *bool             `json:"ssl"`
		PoolSize   *int              `json:"poolSize"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(plain)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return false
	}
	const sentinel = "AGENTSQL_GATEWAY_MANAGED"
	return config.Host == sentinel && config.Port == 1 && config.Username == sentinel &&
		config.Password == "" && config.Database == sentinel && config.DBSchema == sentinel &&
		config.ExtraJDBC == "" && config.Filename == "" && len(config.Sheets) == 0 &&
		(config.Mode == nil || *config.Mode == "service_name") &&
		(config.Timeout == nil || *config.Timeout == 30) &&
		(config.LowVersion == nil || *config.LowVersion) &&
		(config.SSL == nil || !*config.SSL) &&
		(config.PoolSize == nil || *config.PoolSize == 5)
}
