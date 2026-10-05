package compliance

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	ArchiveManifestName  = "manifest.json"
	ArchiveSchemaVersion = "agentsql.audit-archive/v1"
	// MaximumArchiveBytes is the maximum accepted compressed or expanded archive size.
	MaximumArchiveBytes     = 512 << 20
	archiveMaximumFiles     = 16
	archiveMaximumBytes     = MaximumArchiveBytes
	archiveMaximumManifest  = 1 << 20
	archiveMaximumFileBytes = 256 << 20
)

// ArchiveFile is one payload in a compliance archive.
type ArchiveFile struct {
	Name      string
	MediaType string
	Data      []byte
}

type ArchiveManifestFile struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

type ArchiveManifest struct {
	SchemaVersion string                `json:"schema_version"`
	Kind          string                `json:"kind"`
	GeneratedAt   time.Time             `json:"generated_at"`
	Files         []ArchiveManifestFile `json:"files"`
}

// BuildArchive writes a deterministic-shape ZIP with a manifest covering each
// payload. The manifest is an integrity inventory, not a signature.
func BuildArchive(generatedAt time.Time, files []ArchiveFile) ([]byte, ArchiveManifest, error) {
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}
	generatedAt = generatedAt.UTC().Truncate(time.Second)
	if len(files) == 0 || len(files) > archiveMaximumFiles {
		return nil, ArchiveManifest{}, fmt.Errorf("compliance archive must contain between 1 and %d payload files", archiveMaximumFiles)
	}
	ordered := append([]ArchiveFile(nil), files...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Name < ordered[right].Name })
	manifest := ArchiveManifest{SchemaVersion: ArchiveSchemaVersion, Kind: "agentsql-audit-export", GeneratedAt: generatedAt}
	seen := make(map[string]struct{}, len(ordered))
	var total int64
	for _, file := range ordered {
		if err := validateArchiveName(file.Name); err != nil {
			return nil, ArchiveManifest{}, err
		}
		if file.Name == ArchiveManifestName {
			return nil, ArchiveManifest{}, errors.New("manifest.json is reserved")
		}
		if _, exists := seen[file.Name]; exists {
			return nil, ArchiveManifest{}, fmt.Errorf("duplicate compliance archive file %q", file.Name)
		}
		seen[file.Name] = struct{}{}
		if len(file.Data) > archiveMaximumFileBytes {
			return nil, ArchiveManifest{}, fmt.Errorf("compliance archive file %q exceeds size limit", file.Name)
		}
		if strings.TrimSpace(file.MediaType) == "" {
			return nil, ArchiveManifest{}, fmt.Errorf("compliance archive file %q has no media type", file.Name)
		}
		total += int64(len(file.Data))
		if total > archiveMaximumBytes {
			return nil, ArchiveManifest{}, errors.New("compliance archive payload exceeds size limit")
		}
		digest := sha256.Sum256(file.Data)
		manifest.Files = append(manifest.Files, ArchiveManifestFile{
			Name: file.Name, MediaType: strings.TrimSpace(file.MediaType), Size: int64(len(file.Data)), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, ArchiveManifest{}, fmt.Errorf("encode compliance archive manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if len(manifestBytes) > archiveMaximumManifest {
		return nil, ArchiveManifest{}, errors.New("compliance archive manifest exceeds size limit")
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	writeFile := func(name, mediaType string, contents []byte) error {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(generatedAt)
		header.SetMode(0o600)
		header.Comment = mediaType
		destination, createErr := writer.CreateHeader(header)
		if createErr != nil {
			return createErr
		}
		written, writeErr := destination.Write(contents)
		if writeErr == nil && written != len(contents) {
			writeErr = io.ErrShortWrite
		}
		return writeErr
	}
	for _, file := range ordered {
		if err := writeFile(file.Name, file.MediaType, file.Data); err != nil {
			_ = writer.Close()
			return nil, ArchiveManifest{}, fmt.Errorf("write compliance archive file %q: %w", file.Name, err)
		}
	}
	if err := writeFile(ArchiveManifestName, "application/json", manifestBytes); err != nil {
		_ = writer.Close()
		return nil, ArchiveManifest{}, fmt.Errorf("write compliance archive manifest: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, ArchiveManifest{}, fmt.Errorf("close compliance archive: %w", err)
	}
	if output.Len() > archiveMaximumBytes {
		return nil, ArchiveManifest{}, errors.New("compliance archive exceeds size limit")
	}
	return output.Bytes(), manifest, nil
}

// VerifyArchive validates names, bounds, membership, sizes, and every SHA-256
// digest. It does not authenticate the archive producer.
func VerifyArchive(contents []byte) (ArchiveManifest, error) {
	if len(contents) == 0 || len(contents) > archiveMaximumBytes {
		return ArchiveManifest{}, errors.New("compliance archive size is invalid")
	}
	reader, err := zip.NewReader(bytes.NewReader(contents), int64(len(contents)))
	if err != nil {
		return ArchiveManifest{}, fmt.Errorf("open compliance archive: %w", err)
	}
	if len(reader.File) < 2 || len(reader.File) > archiveMaximumFiles+1 {
		return ArchiveManifest{}, errors.New("compliance archive file count is invalid")
	}
	files := make(map[string]*zip.File, len(reader.File))
	var declaredTotal uint64
	for _, file := range reader.File {
		if err := validateArchiveName(file.Name); err != nil {
			return ArchiveManifest{}, err
		}
		if !file.Mode().IsRegular() || file.Flags&1 != 0 {
			return ArchiveManifest{}, fmt.Errorf("unsupported compliance archive entry %q", file.Name)
		}
		if _, exists := files[file.Name]; exists {
			return ArchiveManifest{}, fmt.Errorf("duplicate compliance archive entry %q", file.Name)
		}
		if file.UncompressedSize64 > archiveMaximumFileBytes {
			return ArchiveManifest{}, fmt.Errorf("compliance archive entry %q exceeds size limit", file.Name)
		}
		declaredTotal += file.UncompressedSize64
		if declaredTotal > archiveMaximumBytes {
			return ArchiveManifest{}, errors.New("compliance archive expanded size exceeds limit")
		}
		files[file.Name] = file
	}
	manifestFile, exists := files[ArchiveManifestName]
	if !exists || manifestFile.UncompressedSize64 > archiveMaximumManifest {
		return ArchiveManifest{}, errors.New("compliance archive manifest is missing or too large")
	}
	manifestBytes, err := readZipFileBounded(manifestFile, archiveMaximumManifest)
	if err != nil {
		return ArchiveManifest{}, fmt.Errorf("read compliance archive manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest ArchiveManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ArchiveManifest{}, fmt.Errorf("decode compliance archive manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ArchiveManifest{}, err
	}
	if manifest.SchemaVersion != ArchiveSchemaVersion || manifest.Kind != "agentsql-audit-export" || manifest.GeneratedAt.IsZero() {
		return ArchiveManifest{}, errors.New("compliance archive manifest identity is invalid")
	}
	if len(manifest.Files) != len(files)-1 || len(manifest.Files) == 0 {
		return ArchiveManifest{}, errors.New("compliance archive manifest membership is incomplete")
	}
	manifestNames := make(map[string]struct{}, len(manifest.Files))
	previousName := ""
	for _, expected := range manifest.Files {
		if err := validateArchiveName(expected.Name); err != nil || expected.Name == ArchiveManifestName {
			return ArchiveManifest{}, errors.New("compliance archive manifest contains an invalid file name")
		}
		if previousName != "" && expected.Name <= previousName {
			return ArchiveManifest{}, errors.New("compliance archive manifest files are not uniquely sorted")
		}
		previousName = expected.Name
		if expected.MediaType == "" || expected.Size < 0 || len(expected.SHA256) != sha256.Size*2 || strings.ToLower(expected.SHA256) != expected.SHA256 {
			return ArchiveManifest{}, fmt.Errorf("compliance archive manifest metadata for %q is invalid", expected.Name)
		}
		if _, err := hex.DecodeString(expected.SHA256); err != nil {
			return ArchiveManifest{}, fmt.Errorf("compliance archive manifest digest for %q is invalid", expected.Name)
		}
		file, exists := files[expected.Name]
		if !exists || int64(file.UncompressedSize64) != expected.Size {
			return ArchiveManifest{}, fmt.Errorf("compliance archive file %q is missing or has the wrong size", expected.Name)
		}
		data, err := readZipFileBounded(file, archiveMaximumFileBytes)
		if err != nil {
			return ArchiveManifest{}, fmt.Errorf("read compliance archive file %q: %w", expected.Name, err)
		}
		digest := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(digest[:]), expected.SHA256) {
			return ArchiveManifest{}, fmt.Errorf("compliance archive SHA-256 mismatch for %q", expected.Name)
		}
		manifestNames[expected.Name] = struct{}{}
	}
	for name := range files {
		if name == ArchiveManifestName {
			continue
		}
		if _, exists := manifestNames[name]; !exists {
			return ArchiveManifest{}, fmt.Errorf("unlisted compliance archive file %q", name)
		}
	}
	return manifest, nil
}

func validateArchiveName(name string) error {
	if name == "" || len(name) > 255 || !strings.EqualFold(name, path.Base(name)) || strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		return fmt.Errorf("unsafe compliance archive file name %q", name)
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return fmt.Errorf("unsafe compliance archive file name %q", name)
	}
	return nil
}

func readZipFileBounded(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	contents, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, errors.New("expanded entry exceeds size limit")
	}
	return contents, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("compliance archive manifest contains trailing JSON")
		}
		return fmt.Errorf("decode compliance archive manifest trailer: %w", err)
	}
	return nil
}
