package config

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/stretchr/testify/require"
)

func TestResolveRedactionPrecedenceAndByteLength(t *testing.T) {
	tests := []struct {
		name       string
		yamlKey    string
		envKey     string
		envPresent bool
		want       string
		wantErr    error
	}{
		{name: "yaml only", yamlKey: "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", want: "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"},
		{name: "environment only", envKey: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", envPresent: true, want: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		{name: "empty environment is rejected", yamlKey: "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", envPresent: true, wantErr: ErrInvalidRedactionHashKey},
		{name: "short yaml", yamlKey: "tiny-secret", wantErr: ErrInvalidRedactionHashKey},
		{name: "short environment", yamlKey: "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", envKey: "tiny-secret", envPresent: true, wantErr: ErrInvalidRedactionHashKey},
		{name: "exact boundary", yamlKey: "12345678901234567890123456789012", want: "12345678901234567890123456789012"},
		{name: "multibyte counts bytes", yamlKey: "密钥密钥密钥密钥密钥密钥密钥密钥", want: "密钥密钥密钥密钥密钥密钥密钥密钥"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Redaction: RedactionConfig{HashKey: test.yamlKey}}
			resolved, err := ResolveRedaction(&cfg, func(name string) (string, bool) {
				switch name {
				case RedactionHashKeysFileEnv:
					return "", false
				case RedactionHashKeyEnv:
					return test.envKey, test.envPresent
				default:
					t.Fatalf("unexpected environment lookup %q", name)
					return "", false
				}
			})
			if test.wantErr != nil {
				if test.envPresent && test.envKey == "" {
					require.ErrorContains(t, err, "present but empty")
				} else {
					require.ErrorIs(t, err, test.wantErr)
					require.ErrorIs(t, err, mask.ErrHashKeyTooShort)
				}
				if test.yamlKey != "" {
					require.NotContains(t, err.Error(), test.yamlKey)
				}
				if test.envKey != "" {
					require.NotContains(t, err.Error(), test.envKey)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, resolved.HashKey)
		})
	}
}

func TestResolveRedactionNilInputs(t *testing.T) {
	_, err := ResolveRedaction(nil, nil)
	require.Error(t, err)
	cfg := Config{Redaction: RedactionConfig{HashKey: "12345678901234567890123456789012"}}
	resolved, err := ResolveRedaction(&cfg, nil)
	require.NoError(t, err)
	require.Equal(t, cfg.Redaction, resolved)
}
