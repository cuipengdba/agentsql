package chatbi

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
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
