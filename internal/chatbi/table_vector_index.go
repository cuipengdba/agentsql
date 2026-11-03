package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// A table index stores only vectors derived from the schema currently visible
// to this principal. The encrypted object's scope and owner are authenticated
// by the Store; neither table text nor its fingerprint appears in the key.
func (s *Store) tableVectorKey(p Principal, datasource ID, table modelTableCandidate) objectKey {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte("agentsql:chatbi:table-vector-key:v1\x00"))
	_, _ = mac.Write([]byte(datasource))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(table.key))
	return objectKey{"table-vector-v1", string(p.Workspace), string(p.User.ID), hex.EncodeToString(mac.Sum(nil))}
}

func (s *Store) tableVectorFingerprint(modelIdentity, text string) string {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte("agentsql:chatbi:table-vector-content:v1\x00"))
	_, _ = mac.Write([]byte(modelIdentity))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(text))
	return hex.EncodeToString(mac.Sum(nil))
}

func encodeTableVectors(ctx context.Context, model EmbeddingModel, tables []modelTableCandidate) ([][]float32, error) {
	result := make([][]float32, len(tables))
	for start := 0; start < len(tables); start += 32 {
		end := min(start+32, len(tables))
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = tables[index].text
		}
		vectors, err := model.Encode(ctx, texts)
		if err != nil || len(vectors) != len(texts) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrUnavailable
		}
		for offset, vector := range vectors {
			if _, valid := embeddingVectorNorm(vector); !valid {
				return nil, ErrUnavailable
			}
			result[start+offset] = vector
		}
	}
	return result, nil
}

// indexedTableVectors is called only with the current gateway-authorized
// columns and remarks. A changed column, remark, user, source, or model cannot
// match an old index entry. Storage failure makes preselection keep the entire
// authorized schema, and the caller rechecks the permission snapshot afterward.
func (s *Store) indexedTableVectors(ctx context.Context, p Principal, datasource ID,
	model EmbeddingModel, tables []modelTableCandidate) ([][]float32, error) {
	identity, cacheable := model.(interface{ persistentEmbeddingIdentity() string })
	if s == nil || !cacheable || identity.persistentEmbeddingIdentity() == "" ||
		!p.Workspace.Valid() || !p.User.ID.Valid() || !datasource.Valid() {
		return encodeTableVectors(ctx, model, tables)
	}
	result := make([][]float32, len(tables))
	keys := make([]objectKey, len(tables))
	fingerprints := make([]string, len(tables))
	revisions := make([]int64, len(tables))
	missing := make([]int, 0, len(tables))
	for index, table := range tables {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keys[index] = s.tableVectorKey(p, datasource, table)
		fingerprints[index] = s.tableVectorFingerprint(identity.persistentEmbeddingIdentity(), table.text)
		var saved indexedVectorRecord
		revision, err := s.get(ctx, keys[index], &saved)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		revisions[index] = revision
		if err == nil && saved.Fingerprint == fingerprints[index] {
			if _, valid := embeddingVectorNorm(saved.Vector); !valid {
				return nil, ErrUnavailable
			}
			result[index] = saved.Vector
			continue
		}
		missing = append(missing, index)
	}
	if len(missing) == 0 {
		return result, nil
	}
	toEncode := make([]modelTableCandidate, len(missing))
	for offset, index := range missing {
		toEncode[offset] = tables[index]
	}
	vectors, err := encodeTableVectors(ctx, model, toEncode)
	if err != nil {
		return nil, err
	}
	writes := make([]vectorIndexWrite, 0, len(missing))
	for offset, index := range missing {
		result[index] = vectors[offset]
		writes = append(writes, vectorIndexWrite{keys[index], revisions[index], fingerprints[index], vectors[offset]})
	}
	if err := s.putVectorBatch(ctx, writes); err != nil {
		return nil, err
	}
	return result, nil
}
