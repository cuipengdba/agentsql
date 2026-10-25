package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/chatbi"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestChatBIStandardInstallationCannotAdvertiseAdminIdentity(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "chatbi.yaml")
	databasePath := filepath.Join(dir, "chatbi.db")
	config := "listen: 127.0.0.1:8000\nstore_path: " + filepath.ToSlash(databasePath) + "\nsecret_env: AGENTSQL_CHATBI_TEST_SECRET\ngateway: http://127.0.0.1:7780/mcp\nallow_gateway_http: true\n"
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))
	t.Setenv("AGENTSQL_CHATBI_TEST_SECRET", "01234567890123456789012345678901")
	t.Setenv("AGENTSQL_CHATBI_TEST_PASSWORD", "Synthetic-demo-password-42")
	command := newChatBICommand(zerolog.Nop())
	command.SetOut(io.Discard)
	command.SetArgs([]string{"init", "--config", configPath, "--admin-user", "demo", "--standard-user", "--password-env", "AGENTSQL_CHATBI_TEST_PASSWORD"})
	require.NoError(t, command.Execute())
	loaded, err := chatbi.LoadConfig(configPath)
	require.NoError(t, err)
	service, err := chatbi.Assemble(context.Background(), loaded)
	require.NoError(t, err)
	defer service.Store.Close()
	user, _, err := service.Store.User(context.Background(), "1")
	require.NoError(t, err)
	require.False(t, user.Admin)
	require.False(t, user.AllowLegacyTokens)
	require.Equal(t, 0, user.Memberships[0].Weight)
	_, _, err = service.Identity.Login(context.Background(), "demo", "Synthetic-demo-password-42")
	require.NoError(t, err)
	command = newChatBICommand(zerolog.Nop())
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"init", "--config", configPath, "--password-env", "AGENTSQL_CHATBI_TEST_PASSWORD"})
	require.Error(t, command.Execute(), "reinitialization must not promote the existing identity")
	user, _, err = service.Store.User(context.Background(), "1")
	require.NoError(t, err)
	require.False(t, user.Admin)
}
