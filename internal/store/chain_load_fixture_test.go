package store

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const chainLoadFixtureSeed int64 = 20260924

//go:embed testdata/chain_load_fixture.json
var chainLoadFixtureBytes []byte

type chainLoadFixtureManifest struct {
	Seed            int64       `json:"seed"`
	Count           int         `json:"count"`
	OrderedBytes    int         `json:"ordered_bytes"`
	SHA256          string      `json:"sha256"`
	DigestCanonical string      `json:"digest_canonical"`
	Distribution    map[int]int `json:"distribution"`
}

type chainLoadFixtureFile struct {
	Manifest  chainLoadFixtureManifest `json:"manifest"`
	Templates []json.RawMessage        `json:"templates"`
}

type chainLoadFixture struct {
	manifest chainLoadFixtureManifest
	logs     []model.AuditLog
	sizes    []int
}

func loadChainLoadFixture() (chainLoadFixture, error) {
	var encoded chainLoadFixtureFile
	if err := json.Unmarshal(chainLoadFixtureBytes, &encoded); err != nil {
		return chainLoadFixture{}, fmt.Errorf("decode chain load fixture: %w", err)
	}
	if encoded.Manifest.Seed != chainLoadFixtureSeed || encoded.Manifest.Count != 1000 ||
		len(encoded.Templates) != encoded.Manifest.Count {
		return chainLoadFixture{}, fmt.Errorf("unexpected fixture seed/count: seed=%d manifest=%d templates=%d",
			encoded.Manifest.Seed, encoded.Manifest.Count, len(encoded.Templates))
	}
	digest := sha256.New()
	logs := make([]model.AuditLog, len(encoded.Templates))
	sizes := make([]int, len(encoded.Templates))
	distribution := make(map[int]int)
	orderedBytes := 0
	for index, raw := range encoded.Templates {
		var compact []byte
		buffer := make([]byte, 0, len(raw))
		compactBuffer := append(buffer, raw...)
		if !json.Valid(compactBuffer) {
			return chainLoadFixture{}, fmt.Errorf("fixture template %d is invalid JSON", index)
		}
		compact = compactBuffer
		if err := json.Unmarshal(compact, &logs[index]); err != nil {
			return chainLoadFixture{}, fmt.Errorf("decode fixture template %d: %w", index, err)
		}
		if logs[index].EventUUID == nil || len(*logs[index].EventUUID) != 36 {
			return chainLoadFixture{}, fmt.Errorf("fixture template %d has invalid event UUID", index)
		}
		sizes[index] = len(compact)
		distribution[len(compact)]++
		orderedBytes += len(compact)
		_, _ = digest.Write(compact)
	}
	actualSHA := hex.EncodeToString(digest.Sum(nil))
	if orderedBytes != encoded.Manifest.OrderedBytes || actualSHA != encoded.Manifest.SHA256 {
		return chainLoadFixture{}, fmt.Errorf("fixture manifest mismatch: bytes=%d/%d sha256=%s/%s",
			orderedBytes, encoded.Manifest.OrderedBytes, actualSHA, encoded.Manifest.SHA256)
	}
	if !equalIntCounts(distribution, encoded.Manifest.Distribution) {
		return chainLoadFixture{}, fmt.Errorf("fixture distribution mismatch: got %v want %v", distribution, encoded.Manifest.Distribution)
	}
	return chainLoadFixture{manifest: encoded.Manifest, logs: logs, sizes: sizes}, nil
}

func equalIntCounts(left, right map[int]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (fixture chainLoadFixture) auditLog(scenario string, index int64) (model.AuditLog, int) {
	templateIndex := int(index % int64(len(fixture.logs)))
	if templateIndex < 0 {
		templateIndex += len(fixture.logs)
	}
	log := fixture.logs[templateIndex]
	eventUUID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%d", scenario, index))).String()
	log.EventUUID = &eventUUID
	encoded, err := json.Marshal(log)
	if err != nil || len(encoded) != fixture.sizes[templateIndex] {
		panic(fmt.Sprintf("materialize chain load fixture template %d: bytes=%d want=%d err=%v",
			templateIndex, len(encoded), fixture.sizes[templateIndex], err))
	}
	return log, len(encoded)
}

func TestChainLoadFixtureManifest(t *testing.T) {
	fixture, err := loadChainLoadFixture()
	require.NoError(t, err)
	require.Equal(t, map[int]int{512: 250, 1536: 500, 3072: 199, 8192: 50, 16384: 1}, fixture.manifest.Distribution)
	ordered := append([]int(nil), fixture.sizes...)
	sort.Ints(ordered)
	require.Equal(t, 1536, ordered[int(math.Ceil(0.50*float64(len(ordered))))-1])
	require.Equal(t, 8192, ordered[int(math.Ceil(0.95*float64(len(ordered))))-1])
	for index := int64(0); index < int64(len(fixture.logs)); index++ {
		_, size := fixture.auditLog("fixture-assert", index)
		require.Equal(t, fixture.sizes[index], size)
	}
}
