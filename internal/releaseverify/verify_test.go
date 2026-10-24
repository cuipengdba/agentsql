package releaseverify

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type fixture struct {
	dir, key string
	private  ed25519.PrivateKey
	public   PublicKey
	files    map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	f := &fixture{dir: t.TempDir(), key: filepath.Join(t.TempDir(), "trusted.json"), private: private, public: PublicKey{1, "Ed25519", "release", "sha256:" + digest(pub), base64.RawStdEncoding.EncodeToString(pub)}, files: map[string][]byte{}}
	key, _ := json.Marshal(f.public)
	require.NoError(t, os.WriteFile(f.key, key, 0600))
	for _, name := range Names("v4.0.0") {
		f.files[name] = []byte("fixture " + name + "\n")
	}
	f.files["ed25519-release-public-key.json"] = key
	f.files["agentsql-v4.0.0.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"agentsql","documentNamespace":"https://github.com/cuipengdba/agentsql/releases/download/v4.0.0/agentsql-v4.0.0.spdx.json","packages":[{"name":"fixture"}]}`)
	for _, arch := range []string{"amd64", "arm64"} {
		name := "agentsql-v4.0.0-linux-" + arch + ".tar.gz"
		f.files[name+".sha256"] = []byte(digest(f.files[name]) + "  " + name + "\n")
	}
	subjects := []map[string]any{}
	for _, name := range []string{"agentsql-v4.0.0-linux-amd64.tar.gz", "agentsql-v4.0.0-linux-amd64.tar.gz.sha256", "agentsql-v4.0.0-linux-arm64.tar.gz", "agentsql-v4.0.0-linux-arm64.tar.gz.sha256", "agentsql-v4.0.0.spdx.json", "install.sh", "go-version-metadata.txt", "SBOM-GENERATION.txt"} {
		subjects = append(subjects, map[string]any{"name": name, "size": len(f.files[name]), "digest": map[string]string{"sha256": digest(f.files[name])}})
	}
	f.files["provenance.json"], err = json.Marshal(map[string]any{"schemaVersion": 1, "documentType": "AgentSQL official release provenance", "version": "v4.0.0", "source": map[string]any{"repository": "https://github.com/cuipengdba/agentsql", "commit": strings.Repeat("a", 40), "dirtyAtBuild": false}, "subjects": subjects})
	require.NoError(t, err)
	f.seal(t)
	return f
}

func (f *fixture) sign(t *testing.T, name string) {
	t.Helper()
	payload := f.files[name]
	sig := signature{1, "Ed25519", "release", f.public.KeyID, name, digest(payload), "raw artifact bytes", base64.RawStdEncoding.EncodeToString(ed25519.Sign(f.private, payload))}
	f.files[name+".sig.json"], _ = json.Marshal(sig)
}

func (f *fixture) seal(t *testing.T) {
	t.Helper()
	f.sign(t, "agentsql-v4.0.0.spdx.json")
	f.sign(t, "provenance.json")
	var lines strings.Builder
	for _, name := range Names("v4.0.0") {
		if name != "SHA256SUMS" && name != "SHA256SUMS.sig.json" {
			lines.WriteString(digest(f.files[name]) + "  " + name + "\n")
		}
	}
	f.files["SHA256SUMS"] = []byte(lines.String())
	f.sign(t, "SHA256SUMS")
	f.write(t)
}

func (f *fixture) write(t *testing.T) {
	t.Helper()
	for name, data := range f.files {
		require.NoError(t, os.WriteFile(filepath.Join(f.dir, name), data, 0600))
	}
}

func TestVerifyAuthenticReleaseAndCancellation(t *testing.T) {
	f := newFixture(t)
	report, err := Verify(context.Background(), f.dir, f.key, "v4.0.0")
	require.NoError(t, err)
	require.Equal(t, 15, report.Assets)
	require.Equal(t, 3, report.Signatures)
	require.True(t, report.ReadOnly)
	require.Equal(t, strings.Repeat("a", 40), report.SourceCommit)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Verify(ctx, f.dir, f.key, "v4.0.0")
	require.Error(t, err)
}

func TestVerifyRejectsUntrustedOrAmbiguousReleases(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *fixture)
	}{
		{"tampered package", func(t *testing.T, f *fixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, "agentsql-v4.0.0-linux-amd64.tar.gz"), []byte("evil"), 0600))
		}},
		{"missing", func(t *testing.T, f *fixture) { require.NoError(t, os.Remove(filepath.Join(f.dir, "install.sh"))) }},
		{"extra", func(t *testing.T, f *fixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.dir, "extra"), nil, 0600))
		}},
		{"key from bundle", func(t *testing.T, f *fixture) { f.key = filepath.Join(f.dir, "ed25519-release-public-key.json") }},
		{"wrong key", func(t *testing.T, f *fixture) { f.key = newFixture(t).key }},
		{"DEV key", func(t *testing.T, f *fixture) {
			data, err := os.ReadFile(f.key)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(f.key, []byte(strings.Replace(string(data), `"release"`, `"development"`, 1)), 0600))
		}},
		{"bad signature", func(t *testing.T, f *fixture) { f.files["SHA256SUMS.sig.json"] = []byte(`{}`); f.write(t) }},
		{"duplicate manifest", func(t *testing.T, f *fixture) {
			line := strings.Split(string(f.files["SHA256SUMS"]), "\n")[0]
			f.files["SHA256SUMS"] = append(f.files["SHA256SUMS"], []byte(line+"\n")...)
			f.sign(t, "SHA256SUMS")
			f.write(t)
		}},
		{"traversal", func(t *testing.T, f *fixture) {
			f.files["SHA256SUMS"] = []byte(strings.Replace(string(f.files["SHA256SUMS"]), "  install.sh", "  ../install.sh", 1))
			f.sign(t, "SHA256SUMS")
			f.write(t)
		}},
		{"different provenance version", func(t *testing.T, f *fixture) {
			f.files["provenance.json"] = []byte(strings.Replace(string(f.files["provenance.json"]), `"version":"v4.0.0"`, `"version":"v3.9.0"`, 1))
			f.seal(t)
		}},
		{"dirty source", func(t *testing.T, f *fixture) {
			f.files["provenance.json"] = []byte(strings.Replace(string(f.files["provenance.json"]), `"dirtyAtBuild":false`, `"dirtyAtBuild":true`, 1))
			f.seal(t)
		}},
		{"duplicate JSON field", func(t *testing.T, f *fixture) {
			f.files["provenance.json"] = []byte(strings.Replace(string(f.files["provenance.json"]), `"schemaVersion":1`, `"schemaVersion":0,"schemaVersion":1`, 1))
			f.seal(t)
		}},
		{"wrong package sidecar", func(t *testing.T, f *fixture) {
			f.files["agentsql-v4.0.0-linux-amd64.tar.gz.sha256"] = []byte(strings.Repeat("0", 64) + "  agentsql-v4.0.0-linux-amd64.tar.gz\n")
			f.seal(t)
		}},
		{"wrong subject", func(t *testing.T, f *fixture) {
			f.files["provenance.json"] = []byte(strings.Replace(string(f.files["provenance.json"]), `"name":"install.sh"`, `"name":"unknown.sh"`, 1))
			f.seal(t)
		}},
		{"symlink", func(t *testing.T, f *fixture) {
			path := filepath.Join(f.dir, "install.sh")
			require.NoError(t, os.Remove(path))
			if err := os.Symlink(f.key, path); err != nil {
				t.Skip("symlink privilege unavailable")
			}
		}},
		{"oversized document", func(t *testing.T, f *fixture) {
			file, err := os.OpenFile(filepath.Join(f.dir, "provenance.json"), os.O_WRONLY, 0600)
			require.NoError(t, err)
			require.NoError(t, file.Truncate(maxDocument+1))
			require.NoError(t, file.Close())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			test.mutate(t, f)
			_, err := Verify(context.Background(), f.dir, f.key, "v4.0.0")
			require.Error(t, err)
		})
	}
}

func TestManifestRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"../x", "x/y", "x\\y", "C:x", ".", "..", "x\x00", " x", "x "} {
		_, err := parseManifest([]byte(strings.Repeat("a", 64) + "  " + name + "\n"))
		require.Error(t, err, name)
	}
}
