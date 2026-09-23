package mask

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
)

var (
	benchmarkFingerprint string
	benchmarkVersion     int
	benchmarkResult      model.QueryResult
	benchmarkReport      RedactReport
)

func BenchmarkFingerprint(b *testing.B) {
	key := []byte("0123456789abcdef0123456789abcdef")
	legacy, err := newLegacyHasher(key)
	if err != nil {
		b.Fatal(err)
	}
	versioned, err := newVersionedHasher(2, key)
	if err != nil {
		b.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		hasher ActiveHasher
	}{{"v1", legacy}, {"v2", versioned}} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				benchmarkFingerprint = test.hasher.Fingerprint("benchmark-plaintext")
			}
		})
	}
}

func BenchmarkParseFingerprint(b *testing.B) {
	for _, test := range []struct {
		name      string
		candidate string
	}{{"valid_v1", "h." + strings.Repeat("a", 32)}, {"valid_v2", "h.2." + strings.Repeat("a", 32)}, {"invalid", "h.02." + strings.Repeat("a", 32)}} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				benchmarkVersion, _ = ParseFingerprint(test.candidate)
			}
		})
	}
}

func BenchmarkRedactHashTable(b *testing.B) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, version := range []int{1, 2} {
		plan, err := BuildRedactionPlan(version, map[int][]byte{version: key})
		if err != nil {
			b.Fatal(err)
		}
		redactor, err := NewRedactor(
			[]Rule{{Column: "secret", SensitiveType: TypeGeneric, Algorithm: AlgoHash}},
			WithRedactionPlan(plan),
		)
		if err != nil {
			b.Fatal(err)
		}
		for _, cells := range []int{1, 100, 1000} {
			rows := make([][]string, cells)
			for index := range rows {
				rows[index] = []string{"value-" + strconv.Itoa(index)}
			}
			input := model.QueryResult{Columns: []string{"secret"}, Rows: rows, RowCount: cells}
			b.Run("v"+strconv.Itoa(version)+"/cells_"+strconv.Itoa(cells), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					benchmarkResult, benchmarkReport = redactor.Apply(input)
				}
			})
		}
	}
}
