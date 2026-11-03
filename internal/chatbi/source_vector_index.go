package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// sourceVectorRecord contains no source text. The object store encrypts the
// vector and the content fingerprint with the user's workspace scope as AAD.
type sourceVectorRecord struct {
	Fingerprint string    `json:"fingerprint"`
	Vector      []float32 `json:"vector"`
}

func (s *Store) sourceVectorKey(p Principal, entry assistantCatalogEntry) objectKey {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte("agentsql:chatbi:source-vector-key:v1\x00"))
	_, _ = mac.Write([]byte(entry.CatalogID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(entry.Datasource))
	return objectKey{"source-vector-v1", string(p.Workspace), string(p.User.ID), hex.EncodeToString(mac.Sum(nil))}
}

func (s *Store) sourceVectorFingerprint(modelIdentity, text string) string {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte("agentsql:chatbi:source-vector-content:v1\x00"))
	_, _ = mac.Write([]byte(modelIdentity))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(text))
	return hex.EncodeToString(mac.Sum(nil))
}

// indexedSourceVectors is called only with fresh, authorized source content.
// A changed source or pinned model cannot match a previous fingerprint. Cache
// failures fall back to the complete authorized candidate set in the caller.
func (s *Store) indexedSourceVectors(ctx context.Context, p Principal, model EmbeddingModel,
	entries []assistantCatalogEntry, texts []string) ([][]float32, error) {
	identity, cacheable := model.(interface{ persistentEmbeddingIdentity() string })
	if s == nil || !cacheable || identity.persistentEmbeddingIdentity() == "" ||
		len(entries) != len(texts) || !p.Workspace.Valid() || !p.User.ID.Valid() {
		return model.Encode(ctx, texts)
	}
	result := make([][]float32, len(texts))
	keys := make([]objectKey, len(texts))
	fingerprints := make([]string, len(texts))
	revisions := make([]int64, len(texts))
	missingAt := make([]int, 0, len(texts))
	missingTexts := make([]string, 0, len(texts))
	for index, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := s.sourceVectorKey(p, entry)
		fingerprint := s.sourceVectorFingerprint(identity.persistentEmbeddingIdentity(), texts[index])
		keys[index], fingerprints[index] = key, fingerprint
		var saved sourceVectorRecord
		revision, err := s.get(ctx, key, &saved)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		revisions[index] = revision
		if err == nil && saved.Fingerprint == fingerprint {
			if _, valid := embeddingVectorNorm(saved.Vector); !valid {
				return nil, ErrUnavailable
			}
			result[index] = append([]float32(nil), saved.Vector...)
			continue
		}
		missingAt = append(missingAt, index)
		missingTexts = append(missingTexts, texts[index])
	}
	if len(missingTexts) == 0 {
		return result, nil
	}
	vectors, err := model.Encode(ctx, missingTexts)
	if err != nil || len(vectors) != len(missingTexts) {
		return nil, ErrUnavailable
	}
	for offset, vector := range vectors {
		if _, valid := embeddingVectorNorm(vector); !valid {
			return nil, ErrUnavailable
		}
		index := missingAt[offset]
		result[index] = append([]float32(nil), vector...)
		if err := s.put(ctx, keys[index], sourceVectorRecord{fingerprints[index], vector}, revisions[index]); err != nil && !errors.Is(err, ErrConflict) {
			return nil, err
		}
	}
	return result, nil
}
