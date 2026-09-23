package auditchain

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
)

//go:embed testdata/b6-golden-vectors.json
var goldenFixture []byte

type fixtureFile struct {
	SchemaVersion  int             `json:"schema_version"`
	GenesisPrevHex string          `json:"genesis_prev_hex"`
	Vectors        []fixtureVector `json:"vectors"`
}

type fixtureVector struct {
	Name     string          `json:"name"`
	Input    fixtureInput    `json:"input"`
	Expected fixtureExpected `json:"expected"`
}

type fixtureInput struct {
	Constant        string  `json:"constant"`
	InstanceID      string  `json:"instance_id"`
	ChainID         string  `json:"chain_id"`
	AlgorithmMode   string  `json:"algorithm_mode"`
	ChainKeyVersion int64   `json:"chain_key_version"`
	ChainSeq        int64   `json:"chain_seq"`
	PrevHash        string  `json:"prev_hash"`
	KeyHex          *string `json:"key_hex"`
	Row             Row     `json:"row"`
}

type fixtureExpected struct {
	GenesisPrevHex  string `json:"genesis_prev_hex"`
	CanonicalTLVHex string `json:"canonical_tlv_hex"`
	EnvelopeHex     string `json:"envelope_hex"`
	SelfHash        string `json:"self_hash"`
}

func TestGoldenVectors(t *testing.T) {
	var fixture fixtureFile
	fixtureJSON := bytes.TrimPrefix(goldenFixture, []byte{0xef, 0xbb, 0xbf})
	if err := json.Unmarshal(fixtureJSON, &fixture); err != nil {
		t.Fatalf("decode embedded fixture: %v", err)
	}
	if fixture.SchemaVersion != 1 {
		t.Fatalf("fixture schema_version = %d, want 1", fixture.SchemaVersion)
	}
	if len(fixture.Vectors) != 8 {
		t.Fatalf("fixture vector count = %d, want 8", len(fixture.Vectors))
	}

	computedHashes := make(map[string]string, len(fixture.Vectors))
	for _, vector := range fixture.Vectors {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			if vector.Name == "V0_genesis" {
				if vector.Input.Constant != GenesisASCII {
					t.Fatalf("genesis input constant = %q, want %q", vector.Input.Constant, GenesisASCII)
				}
				if fixture.GenesisPrevHex != GenesisPrevHex || vector.Expected.GenesisPrevHex != GenesisPrevHex {
					t.Fatalf("fixture genesis values do not equal exported constant %s", GenesisPrevHex)
				}
				digest := sha256.Sum256([]byte(GenesisASCII))
				if got := hex.EncodeToString(digest[:]); got != GenesisPrevHex {
					t.Fatalf("genesis digest = %s, want %s", got, GenesisPrevHex)
				}
				return
			}

			canonical, err := EncodeCanonical(vector.Input.Row)
			if err != nil {
				t.Fatalf("encode canonical: %v", err)
			}
			wantCanonical := mustDecodeHex(t, vector.Expected.CanonicalTLVHex)
			if !bytes.Equal(canonical, wantCanonical) {
				t.Fatalf("canonical bytes differ\n got: %x\nwant: %x", canonical, wantCanonical)
			}
			canonicalItems, err := DecodeTLV(canonical)
			if err != nil {
				t.Fatalf("decode generated canonical: %v", err)
			}
			if len(canonicalItems) != 27 {
				t.Fatalf("canonical item count = %d, want 27", len(canonicalItems))
			}

			prevBytes := mustDecodeHex(t, vector.Input.PrevHash)
			if len(prevBytes) != 32 {
				t.Fatalf("prev_hash length = %d, want 32", len(prevBytes))
			}
			var prevHash [32]byte
			copy(prevHash[:], prevBytes)
			envelopeValue := NewEnvelope(
				vector.Input.InstanceID,
				vector.Input.ChainID,
				vector.Input.AlgorithmMode,
				vector.Input.ChainKeyVersion,
				vector.Input.ChainSeq,
				prevHash,
				canonical,
			)
			envelope, err := EncodeEnvelope(envelopeValue)
			if err != nil {
				t.Fatalf("encode envelope: %v", err)
			}
			wantEnvelope := mustDecodeHex(t, vector.Expected.EnvelopeHex)
			if !bytes.Equal(envelope, wantEnvelope) {
				t.Fatalf("envelope bytes differ\n got: %x\nwant: %x", envelope, wantEnvelope)
			}
			envelopeItems, err := DecodeTLV(envelope)
			if err != nil {
				t.Fatalf("decode generated envelope: %v", err)
			}
			if len(envelopeItems) != 9 {
				t.Fatalf("envelope item count = %d, want 9", len(envelopeItems))
			}

			var key []byte
			if vector.Input.KeyHex != nil {
				key = mustDecodeHex(t, *vector.Input.KeyHex)
			}
			if got := Hash(envelope, key); got != vector.Expected.SelfHash {
				t.Fatalf("self_hash = %s, want %s", got, vector.Expected.SelfHash)
			}
			got, err := HashEnvelope(envelopeValue, envelope, key)
			if err != nil {
				t.Fatalf("hash envelope: %v", err)
			}
			if got != vector.Expected.SelfHash {
				t.Fatalf("mode-selected self_hash = %s, want %s", got, vector.Expected.SelfHash)
			}
			computedHashes[vector.Name] = got
		})
	}

	assertPrevLink(t, fixture.Vectors, "V3_hmac_row2", computedHashes["V2_hmac_row1"])
	assertPrevLink(t, fixture.Vectors, "V4_key_rotation", computedHashes["V3_hmac_row2"])
}

func TestDecodeTLVRejectsMalformed(t *testing.T) {
	tests := []struct {
		name    string
		encoded []byte
	}{
		{name: "nil with payload", encoded: []byte{0x00, 0, 0, 0, 1, 'x'}},
		{name: "empty int", encoded: []byte{0x02, 0, 0, 0, 0}},
		{name: "leading zero", encoded: []byte{0x02, 0, 0, 0, 2, '0', '1'}},
		{name: "negative zero", encoded: []byte{0x02, 0, 0, 0, 2, '-', '0'}},
		{name: "bare minus", encoded: []byte{0x02, 0, 0, 0, 1, '-'}},
		{name: "misplaced minus", encoded: []byte{0x02, 0, 0, 0, 3, '1', '-', '2'}},
		{name: "non digit", encoded: []byte{0x02, 0, 0, 0, 2, '1', 'x'}},
		{name: "unknown tag", encoded: []byte{0x7f, 0, 0, 0, 0}},
		{name: "declared length exceeds input", encoded: []byte{0x01, 0, 0, 0, 2, 'x'}},
		{name: "maximum length cannot fit", encoded: []byte{0x03, 0xff, 0xff, 0xff, 0xff}},
		{name: "trailing partial header", encoded: []byte{0x00, 0, 0, 0, 0, 0x01}},
		{name: "residual header", encoded: []byte{0x00, 0, 0, 0}},
		{name: "invalid utf8 text", encoded: []byte{0x01, 0, 0, 0, 1, 0xff}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeTLV(test.encoded); err == nil {
				t.Fatal("DecodeTLV unexpectedly accepted malformed input")
			}
		})
	}
}

func TestTLVRoundTripAndNilVsEmpty(t *testing.T) {
	encoded, err := EncodeTLV(
		Item{Tag: TagNil},
		Item{Tag: TagText, Payload: []byte{}},
		Item{Tag: TagInt, Payload: []byte("-42")},
		Item{Tag: TagBytes, Payload: []byte{0, 1, 0xff}},
	)
	if err != nil {
		t.Fatalf("encode TLV: %v", err)
	}
	items, err := DecodeTLV(encoded)
	if err != nil {
		t.Fatalf("decode TLV: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("item count = %d, want 4", len(items))
	}
	if items[0].Tag != TagNil || items[1].Tag != TagText || len(items[1].Payload) != 0 {
		t.Fatalf("NIL and empty TEXT were not kept distinct: %#v", items[:2])
	}

	encoded[len(encoded)-1] = 0
	if items[3].Payload[2] != 0xff {
		t.Fatal("decoded payload aliases encoded input")
	}
}

func TestHashModesAndKeyVersions(t *testing.T) {
	envelope := []byte("same-envelope")
	keys := map[int64][]byte{
		1: []byte("version-one-key"),
		2: []byte("version-two-key"),
	}

	keyless := Hash(envelope, nil)
	hmacV1 := Hash(envelope, keys[1])
	hmacV2 := Hash(envelope, keys[2])
	if keyless == hmacV1 || keyless == hmacV2 || hmacV1 == hmacV2 {
		t.Fatalf("expected distinct hashes, got keyless=%s v1=%s v2=%s", keyless, hmacV1, hmacV2)
	}

	selectedVersion := int64(2)
	if got := Hash(envelope, keys[selectedVersion]); got != hmacV2 {
		t.Fatalf("key version %d selected wrong key", selectedVersion)
	}
}

func TestValidateTimestamp(t *testing.T) {
	if err := ValidateTimestamp("2026-09-24T01:02:03.000004Z"); err != nil {
		t.Fatalf("valid timestamp rejected: %v", err)
	}
	invalid := []string{
		"2026-09-24T01:02:03Z",
		"2026-09-24T01:02:03.00004Z",
		"2026-09-24T01:02:03.000004+00:00",
		"2026-02-30T01:02:03.000004Z",
	}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			if err := ValidateTimestamp(value); err == nil {
				t.Fatalf("invalid timestamp %q accepted", value)
			}
		})
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode fixture hex: %v", err)
	}
	return decoded
}

func assertPrevLink(t *testing.T, vectors []fixtureVector, name, want string) {
	t.Helper()
	for _, vector := range vectors {
		if vector.Name == name {
			if vector.Input.PrevHash != want {
				t.Fatalf("%s prev_hash = %s, want prior self_hash %s", name, vector.Input.PrevHash, want)
			}
			return
		}
	}
	t.Fatal(fmt.Sprintf("fixture vector %s not found", name))
}
