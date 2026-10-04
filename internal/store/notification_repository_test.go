package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/cuipengdba/agentsql/internal/notify"
	"github.com/stretchr/testify/require"
)

func TestNotificationRepositorySQLite(t *testing.T) {
	ctx := context.Background()
	opened, err := OpenWithSecret(ctx, filepath.Join(t.TempDir(), "notifications.db"), []byte(testSecret))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, opened.Close()) })

	repository := opened.Notifications()
	initial, err := repository.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, notify.Config{}, initial)

	want := completeNotificationConfig()
	require.NoError(t, repository.Replace(ctx, want))
	got, err := repository.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)

	// A repeated whole-config replacement is idempotent: no duplicate singleton
	// or channel rows, and ordering plus all semantic values remain unchanged.
	require.NoError(t, repository.Replace(ctx, want))
	got, err = repository.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)
	var settingsCount, channelCount int
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_settings").Scan(&settingsCount))
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_channels").Scan(&channelCount))
	require.Equal(t, 1, settingsCount)
	require.Equal(t, len(want.Channels), channelCount)

	var urlCiphertext, bearerCiphertext, headersCiphertext, secretCiphertext string
	require.NoError(t, opened.metaDB.QueryRowContext(ctx, `
SELECT webhook_url_enc, webhook_bearer_token_enc, webhook_headers_enc, webhook_secret_enc
FROM notification_channels WHERE id = 'generic'`).Scan(
		&urlCiphertext, &bearerCiphertext, &headersCiphertext, &secretCiphertext,
	))
	require.NotEqual(t, want.Channels[0].Webhook.URL, urlCiphertext)
	require.NotContains(t, urlCiphertext, "url-token")
	require.NotEqual(t, want.Channels[0].Webhook.BearerToken, bearerCiphertext)
	require.NotEqual(t, `{"Authorization":"header-secret","X-Tenant":"blue"}`, headersCiphertext)
	require.NotEqual(t, want.Channels[0].Webhook.Secret, secretCiphertext)

	// Reordering is also an atomic upsert (not delete/reinsert), so created_at
	// survives while the position uniqueness constraint remains satisfied.
	_, err = opened.metaDB.ExecContext(ctx, `
UPDATE notification_channels SET created_at='2000-01-02 03:04:05' WHERE id='generic'`)
	require.NoError(t, err)
	reordered := want
	reordered.Channels = append([]notify.ChannelConfig(nil), want.Channels...)
	for left, right := 0, len(reordered.Channels)-1; left < right; left, right = left+1, right-1 {
		reordered.Channels[left], reordered.Channels[right] = reordered.Channels[right], reordered.Channels[left]
	}
	require.NoError(t, repository.Replace(ctx, reordered))
	got, err = repository.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, reordered, got)
	var createdAt string
	require.NoError(t, opened.metaDB.QueryRowContext(ctx,
		"SELECT created_at FROM notification_channels WHERE id='generic'",
	).Scan(&createdAt))
	require.Contains(t, createdAt, "2000-01-02")
}

func TestNotificationMigrationsSQLiteFreshAndVersionOneUpgrade(t *testing.T) {
	ctx := context.Background()
	t.Run("fresh combined and separated metadata", func(t *testing.T) {
		for _, separated := range []bool{false, true} {
			path := filepath.Join(t.TempDir(), "fresh.db")
			database, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			require.NoError(t, MigrateMetadata(ctx, database, DialectSQLite, separated))
			current, latest, err := MetadataMigrationVersions(ctx, database, DialectSQLite, separated)
			require.NoError(t, err)
			if separated {
				require.Equal(t, 14, current)
				require.Equal(t, 14, latest)
			} else {
				require.Equal(t, 15, current)
				require.Equal(t, 15, latest)
			}
			assertSQLiteNotificationTables(t, ctx, database)
			require.NoError(t, database.Close())
		}
	})

	t.Run("combined version one upgrades to two", func(t *testing.T) {
		database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
		require.NoError(t, err)
		require.NoError(t, enableSQLiteForeignKeys(ctx, database))
		_, err = database.ExecContext(ctx, sqliteSchemaMigrationsDDL)
		require.NoError(t, err)
		versionOne, err := fs.ReadFile(migrationFiles, "migrations/sqlite/0001_init.sql")
		require.NoError(t, err)
		require.NoError(t, applyMigration(ctx, database, DialectSQLite, 1, string(versionOne)))
		var before int
		require.NoError(t, database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&before))
		require.Equal(t, 1, before)

		require.NoError(t, Migrate(ctx, database, DialectSQLite))
		var after int
		require.NoError(t, database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&after))
		require.Equal(t, 15, after)
		assertSQLiteNotificationTables(t, ctx, database)
		require.NoError(t, database.Close())
	})
}

func assertSQLiteNotificationTables(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	for _, table := range []string{"notification_settings", "notification_channels"} {
		var count int
		require.NoError(t, database.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count))
		require.Equal(t, 1, count, table)
	}
}

func completeNotificationConfig() notify.Config {
	templates := []notify.WebhookTemplate{
		notify.WebhookGeneric, notify.WebhookFeishu, notify.WebhookDingTalk,
		notify.WebhookWeCom, notify.WebhookSlack,
	}
	channels := make([]notify.ChannelConfig, 0, len(templates)+1)
	for index, template := range templates {
		channel := notify.ChannelConfig{
			ID: string(template), Enabled: index != 3, Kind: notify.ChannelWebhook,
			Decisions: []string{"deny", "error", "approve"}, IncludeSQL: index == 0,
			AllowPrivateEndpoints: index == 1,
			Webhook: &notify.WebhookConfig{
				Template:    template,
				URL:         "https://notify.example.test/hook/url-token-" + string(template),
				BearerToken: "bearer-" + string(template),
				Headers:     map[string]string{"Authorization": "header-secret", "X-Tenant": "blue"},
				Secret:      "signing-" + string(template),
			},
		}
		channels = append(channels, channel)
	}
	channels = append(channels, notify.ChannelConfig{
		ID: "siem", Enabled: true, Kind: notify.ChannelSyslog,
		Decisions: []string{"warn", "deny"}, AllowPrivateEndpoints: true,
		Syslog: &notify.SyslogConfig{Host: "siem.internal", Port: 6514, Transport: "tcp", Facility: 16},
	})
	return notify.Config{Enabled: true, QueueSize: 257, Channels: channels}
}
