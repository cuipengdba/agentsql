package chatbi

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// SQLBot Community v1.10.2's browser uses a public, fixed AES-ECB key for
// datasource configuration. This is a wire-format decoder, not a secret or an
// authorization mechanism. Every decoded field is checked against a
// server-owned import receipt before an Excel datasource can be claimed.
func decodeSQLBotExcelConfig(encoded string) (string, []importedFileSheet, error) {
	if encoded == "" || len(encoded) > 65536 {
		return "", nil, errFileImport
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 49152 || len(data)%aes.BlockSize != 0 {
		return "", nil, errFileImport
	}
	block, err := aes.NewCipher([]byte("SQLBot1234567890"))
	if err != nil {
		return "", nil, errFileImport
	}
	plain := make([]byte, len(data))
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], data[offset:offset+aes.BlockSize])
	}
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize {
		return "", nil, errFileImport
	}
	for _, value := range plain[len(plain)-padding:] {
		if int(value) != padding {
			return "", nil, errFileImport
		}
	}
	plain = plain[:len(plain)-padding]
	var config struct {
		Filename  string              `json:"filename"`
		Sheets    []importedFileSheet `json:"sheets"`
		Host      string              `json:"host"`
		Username  string              `json:"username"`
		Password  string              `json:"password"`
		Database  string              `json:"database"`
		DBSchema  string              `json:"dbSchema"`
		ExtraJDBC string              `json:"extraJdbc"`
		Port      int                 `json:"port"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(plain)))
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF ||
		config.Filename == "" || strings.ContainsAny(config.Filename, "/\\\x00") ||
		len(config.Sheets) == 0 || len(config.Sheets) > 8 || config.Host != "" || config.Username != "" ||
		config.Password != "" || config.Database != "" || config.DBSchema != "" || config.ExtraJDBC != "" || config.Port != 0 {
		return "", nil, errFileImport
	}
	for _, sheet := range config.Sheets {
		if sheet.SheetName == "" || !managedFileNamePattern.MatchString(sheet.TableName) || sheet.Rows < 0 || sheet.Rows > 1000 {
			return "", nil, errFileImport
		}
	}
	return config.Filename, config.Sheets, nil
}
