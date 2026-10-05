package compliance

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestArchiveRoundTripAndManifestDigests(t *testing.T) {
	contents, built, err := BuildArchive(time.Date(2026, 10, 5, 3, 4, 5, 0, time.UTC), []ArchiveFile{
		{Name: "summary.json", MediaType: "application/json", Data: []byte("{}\n")},
		{Name: "audit.csv", MediaType: "text/csv", Data: []byte("id\n1\n")},
	})
	require.NoError(t, err)
	verified, err := VerifyArchive(contents)
	require.NoError(t, err)
	require.Equal(t, built, verified)
	require.Equal(t, []string{"audit.csv", "summary.json"}, []string{verified.Files[0].Name, verified.Files[1].Name})
}

func TestArchiveRejectsTraversalDuplicateAndDigestMismatch(t *testing.T) {
	_, _, err := BuildArchive(time.Now(), []ArchiveFile{{Name: "../audit.csv", MediaType: "text/csv", Data: []byte("x")}})
	require.ErrorContains(t, err, "unsafe")
	_, _, err = BuildArchive(time.Now(), []ArchiveFile{{Name: strings.Repeat("a", 256), MediaType: "text/csv", Data: []byte("x")}})
	require.ErrorContains(t, err, "unsafe")
	_, _, err = BuildArchive(time.Now(), []ArchiveFile{
		{Name: "audit.csv", MediaType: "text/csv", Data: []byte("x")},
		{Name: "audit.csv", MediaType: "text/csv", Data: []byte("y")},
	})
	require.ErrorContains(t, err, "duplicate")

	manifest := ArchiveManifest{SchemaVersion: ArchiveSchemaVersion, Kind: "agentsql-audit-export", GeneratedAt: time.Now().UTC(), Files: []ArchiveManifestFile{
		{Name: "audit.csv", MediaType: "text/csv", Size: 1, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
	}}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	corrupt := testZIP(t, map[string][]byte{"audit.csv": []byte("x"), ArchiveManifestName: encoded})
	_, err = VerifyArchive(corrupt)
	require.ErrorContains(t, err, "SHA-256 mismatch")

	unsafe := testZIP(t, map[string][]byte{"../audit.csv": []byte("x"), ArchiveManifestName: encoded})
	_, err = VerifyArchive(unsafe)
	require.ErrorContains(t, err, "unsafe")
}

func TestArchiveRejectsUnlistedFileAndUnknownManifestField(t *testing.T) {
	contents, manifest, err := BuildArchive(time.Now(), []ArchiveFile{{Name: "audit.csv", MediaType: "text/csv", Data: []byte("x")}})
	require.NoError(t, err)
	_ = contents
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	withExtra := testZIP(t, map[string][]byte{"audit.csv": []byte("x"), "extra.txt": []byte("extra"), ArchiveManifestName: encoded})
	_, err = VerifyArchive(withExtra)
	require.ErrorContains(t, err, "membership")

	unknown := append(bytes.TrimSuffix(encoded, []byte("}")), []byte(`,"unexpected":true}`)...)
	withUnknown := testZIP(t, map[string][]byte{"audit.csv": []byte("x"), ArchiveManifestName: unknown})
	_, err = VerifyArchive(withUnknown)
	require.ErrorContains(t, err, "unknown field")
}

func TestArchiveRejectsDeclaredOversizeEntry(t *testing.T) {
	manifest := ArchiveManifest{SchemaVersion: ArchiveSchemaVersion, Kind: "agentsql-audit-export", GeneratedAt: time.Now().UTC(), Files: []ArchiveManifestFile{
		{Name: "audit.csv", MediaType: "text/csv", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5da7e5c3c70c2ce31b35c292e33a047b5"},
	}}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	contents := testZIP(t, map[string][]byte{"audit.csv": []byte("x"), ArchiveManifestName: encoded})

	const centralDirectoryHeaderSize = 46
	for offset := 0; ; {
		relative := bytes.Index(contents[offset:], []byte{'P', 'K', 1, 2})
		require.NotEqual(t, -1, relative, "audit.csv central-directory entry not found")
		offset += relative
		require.GreaterOrEqual(t, len(contents), offset+centralDirectoryHeaderSize)
		nameLength := int(binary.LittleEndian.Uint16(contents[offset+28 : offset+30]))
		extraLength := int(binary.LittleEndian.Uint16(contents[offset+30 : offset+32]))
		commentLength := int(binary.LittleEndian.Uint16(contents[offset+32 : offset+34]))
		end := offset + centralDirectoryHeaderSize + nameLength + extraLength + commentLength
		require.LessOrEqual(t, end, len(contents))
		if string(contents[offset+centralDirectoryHeaderSize:offset+centralDirectoryHeaderSize+nameLength]) == "audit.csv" {
			binary.LittleEndian.PutUint32(contents[offset+24:offset+28], uint32(archiveMaximumFileBytes+1))
			break
		}
		offset = end
	}

	_, err = VerifyArchive(contents)
	require.ErrorContains(t, err, "exceeds size limit")
}

func TestArchiveRejectsSymlinkEntry(t *testing.T) {
	manifest := ArchiveManifest{SchemaVersion: ArchiveSchemaVersion, Kind: "agentsql-audit-export", GeneratedAt: time.Now().UTC(), Files: []ArchiveManifestFile{
		{Name: "audit.csv", MediaType: "text/csv", Size: 1, SHA256: "2d711642b726b04401627ca9fbac32f5da7e5c3c70c2ce31b35c292e33a047b5"},
	}}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "audit.csv", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	require.NoError(t, err)
	_, err = entry.Write([]byte("x"))
	require.NoError(t, err)
	entry, err = writer.Create(ArchiveManifestName)
	require.NoError(t, err)
	_, err = entry.Write(encoded)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	_, err = VerifyArchive(buffer.Bytes())
	require.ErrorContains(t, err, "unsupported compliance archive entry")
}

func testZIP(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, contents := range entries {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		written, err := entry.Write(contents)
		require.NoError(t, err)
		require.Equal(t, len(contents), written)
	}
	require.NoError(t, writer.Close())
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	require.NoError(t, err)
	for _, file := range reader.File {
		opened, openErr := file.Open()
		require.NoError(t, openErr)
		_, copyErr := io.Copy(io.Discard, opened)
		require.NoError(t, copyErr)
		require.NoError(t, opened.Close())
	}
	return buffer.Bytes()
}
