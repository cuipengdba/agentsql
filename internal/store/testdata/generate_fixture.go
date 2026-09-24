//go:build ignore

// Command generate_fixture deterministically regenerates chain_load_fixture.json.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/cuipengdba/agentsql/internal/model"
)

const fixtureSeed int64 = 20260924

type manifest struct {
	Seed            int64       `json:"seed"`
	Count           int         `json:"count"`
	OrderedBytes    int         `json:"ordered_bytes"`
	SHA256          string      `json:"sha256"`
	DigestCanonical string      `json:"digest_canonical"`
	Distribution    map[int]int `json:"distribution"`
}

type fixture struct {
	Manifest  manifest          `json:"manifest"`
	Templates []json.RawMessage `json:"templates"`
}

func main() {
	sizes := make([]int, 0, 1000)
	for _, bucket := range []struct {
		size  int
		count int
	}{
		{size: 512, count: 250},
		{size: 1536, count: 500},
		{size: 3072, count: 199},
		{size: 8192, count: 50},
		{size: 16384, count: 1},
	} {
		for range bucket.count {
			sizes = append(sizes, bucket.size)
		}
	}
	rng := rand.New(rand.NewSource(fixtureSeed))
	rng.Shuffle(len(sizes), func(left, right int) { sizes[left], sizes[right] = sizes[right], sizes[left] })

	templates := make([]json.RawMessage, len(sizes))
	digest := sha256.New()
	orderedBytes := 0
	for index, target := range sizes {
		eventUUID := fmt.Sprintf("00000000-0000-0000-0000-%012d", index)
		details := `{"payload":""}`
		log := model.AuditLog{Decision: "allow", DetailsJSON: &details, EventUUID: &eventUUID}
		base, err := json.Marshal(log)
		must(err)
		padding := target - len(base)
		if padding < 0 {
			panic(fmt.Sprintf("target %d is smaller than base event %d", target, len(base)))
		}
		alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		payload := make([]byte, padding)
		for offset := range payload {
			payload[offset] = alphabet[rng.Intn(len(alphabet))]
		}
		details = `{"payload":"` + string(payload) + `"}`
		body, err := json.Marshal(log)
		must(err)
		if len(body) != target {
			panic(fmt.Sprintf("template %d has %d bytes, want %d", index, len(body), target))
		}
		templates[index] = body
		orderedBytes += len(body)
		_, err = digest.Write(body)
		must(err)
	}

	data, err := json.Marshal(fixture{
		Manifest: manifest{
			Seed: fixtureSeed, Count: len(templates), OrderedBytes: orderedBytes,
			SHA256:          hex.EncodeToString(digest.Sum(nil)),
			DigestCanonical: "sha256(concat(compact_template_json_in_array_order))",
			Distribution:    map[int]int{512: 250, 1536: 500, 3072: 199, 8192: 50, 16384: 1},
		},
		Templates: templates,
	})
	must(err)
	must(os.WriteFile(filepath.Join("internal", "store", "testdata", "chain_load_fixture.json"), append(data, '\n'), 0o644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
