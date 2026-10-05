package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/version"
	"github.com/spf13/cobra"
)

const (
	upgradeManifestEnv = "AGENTSQL_UPGRADE_MANIFEST_URL"
	maxUpgradeManifest = 16 << 10
	maxUpgradeArtifact = 256 << 20
)

type upgradeManifest struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func newUpgradeCommand() *cobra.Command {
	var manifestURL string
	var backupDir string
	var configPath string
	var dryRun, yes bool
	command := &cobra.Command{Use: "upgrade", Short: "Inspect a release; installation is not available"}
	command.PersistentFlags().StringVar(&manifestURL, "manifest-url", "", "HTTPS release manifest URL (or AGENTSQL_UPGRADE_MANIFEST_URL)")
	command.AddCommand(&cobra.Command{
		Use: "check", Short: "Check an HTTPS release manifest", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifest, status, err := checkUpgrade(cmd.Context(), http.DefaultClient, manifestURL)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "current=%s available=%s status=%s\n", version.Version, manifest.Version, status)
			return err
		},
	})
	apply := &cobra.Command{
		Use: "apply", Short: "Validate an upgrade plan without changing files", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return errors.New("live upgrade is unavailable; use --dry-run to validate a plan")
			}
			if yes {
				return errors.New("--yes is not accepted with --dry-run")
			}
			if strings.TrimSpace(backupDir) == "" {
				return errors.New("--backup-dir is required")
			}
			manifest, status, err := checkUpgrade(cmd.Context(), http.DefaultClient, manifestURL)
			if err != nil {
				return err
			}
			if status != "update-available" {
				return fmt.Errorf("upgrade requires a newer version; status=%s", status)
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate current executable: %w", err)
			}
			return dryRunUpgrade(cmd.Context(), http.DefaultClient, manifest, backupDir, configPath, exe, cmd.OutOrStdout())
		},
	}
	apply.Flags().StringVar(&backupDir, "backup-dir", "", "new directory reserved for a future backup")
	apply.Flags().StringVar(&configPath, "config", "config.yaml", "configuration file to include in the plan")
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "verify the artifact and print a read-only plan")
	apply.Flags().BoolVar(&yes, "yes", false, "reserved for a future live upgrade; currently rejected")
	command.AddCommand(apply)
	return command
}

func upgradeURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u == nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return nil, errors.New("upgrade URL must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	return u, nil
}

func upgradeClient(client *http.Client) *http.Client {
	copy := *client
	copy.Timeout = 30 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("upgrade redirects are forbidden") }
	return &copy
}

func fetchUpgrade(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := upgradeClient(client).Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch upgrade resource: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upgrade resource returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read upgrade resource: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("upgrade resource exceeds size limit")
	}
	return data, nil
}

func parseUpgradeVersion(value string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(parts) != 3 {
		return result, errors.New("upgrade version must be vMAJOR.MINOR.PATCH")
	}
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return result, errors.New("invalid upgrade version")
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return result, errors.New("invalid upgrade version")
			}
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, errors.New("invalid upgrade version")
		}
		result[i] = number
	}
	return result, nil
}

func checkUpgrade(ctx context.Context, client *http.Client, address string) (upgradeManifest, string, error) {
	var manifest upgradeManifest
	if strings.TrimSpace(address) == "" {
		address = os.Getenv(upgradeManifestEnv)
	}
	manifestURL, err := upgradeURL(address)
	if err != nil {
		return manifest, "", err
	}
	data, err := fetchUpgrade(ctx, client, manifestURL.String(), maxUpgradeManifest)
	if err != nil {
		return manifest, "", err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, "", fmt.Errorf("invalid upgrade manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return manifest, "", errors.New("upgrade manifest has trailing content")
	}
	artifactURL, err := upgradeURL(manifest.URL)
	if err != nil {
		return manifest, "", fmt.Errorf("invalid artifact URL: %w", err)
	}
	if artifactURL.Scheme != manifestURL.Scheme || artifactURL.Host != manifestURL.Host {
		return manifest, "", errors.New("artifact URL must use the manifest origin")
	}
	if len(manifest.SHA256) != 64 {
		return manifest, "", errors.New("manifest SHA-256 must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return manifest, "", errors.New("manifest SHA-256 is invalid")
	}
	current, err := parseUpgradeVersion(version.Version)
	if err != nil {
		return manifest, "", fmt.Errorf("invalid current version: %w", err)
	}
	available, err := parseUpgradeVersion(manifest.Version)
	if err != nil {
		return manifest, "", err
	}
	for i := range current {
		if available[i] > current[i] {
			return manifest, "update-available", nil
		}
		if available[i] < current[i] {
			return manifest, "older-release", nil
		}
	}
	return manifest, "up-to-date", nil
}

func dryRunUpgrade(ctx context.Context, client *http.Client, manifest upgradeManifest, backupDir, configPath, executable string, output io.Writer) error {
	for name, path := range map[string]string{"executable": executable, "config": configPath} {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", name)
		}
	}
	absolute, err := filepath.Abs(backupDir)
	if err != nil {
		return fmt.Errorf("resolve backup directory: %w", err)
	}
	if absolute == filepath.VolumeName(absolute)+string(filepath.Separator) {
		return errors.New("backup directory cannot be a filesystem root")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return errors.New("backup directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup directory: %w", err)
	}
	parent, err := os.Stat(filepath.Dir(absolute))
	if err != nil {
		return fmt.Errorf("inspect backup parent: %w", err)
	}
	if !parent.IsDir() {
		return errors.New("backup parent must be a directory")
	}
	data, err := fetchUpgrade(ctx, client, manifest.URL, maxUpgradeArtifact)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), manifest.SHA256) {
		return errors.New("artifact SHA-256 mismatch")
	}
	_, err = fmt.Fprintf(output, "dry_run=true version=%s artifact_sha256=%x backup_dir=%s\nplan=backup-current-executable-and-config\nplan=stop-service-and-install-requires-separate-reviewed-procedure\nchanges=none\n", manifest.Version, digest, absolute)
	return err
}
