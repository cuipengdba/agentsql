package chatbi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	return encodeTableVectorsChecked(ctx, model, tables, nil)
}

// The callback is checked around each bounded inference batch. In particular,
// a revoked schema cannot be committed after a slow local model call.
func encodeTableVectorsChecked(ctx context.Context, model EmbeddingModel, tables []modelTableCandidate,
	validate func(context.Context) error) ([][]float32, error) {
	result := make([][]float32, len(tables))
	for start := 0; start < len(tables); start += 32 {
		if err := validateTableIndex(ctx, validate); err != nil {
			return nil, err
		}
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
		if err := validateTableIndex(ctx, validate); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type tableIndexAuthorizationError struct{ error }

func validateTableIndex(ctx context.Context, validate func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if validate != nil {
		if err := validate(ctx); err != nil {
			return tableIndexAuthorizationError{err}
		}
	}
	return ctx.Err()
}

// indexedTableVectors is called only with the current gateway-authorized
// columns and remarks. A changed column, remark, user, source, or model cannot
// match an old index entry. Storage failure makes preselection keep the entire
// authorized schema, and the caller rechecks the permission snapshot afterward.
func (s *Store) indexedTableVectors(ctx context.Context, p Principal, datasource ID,
	model EmbeddingModel, tables []modelTableCandidate, validate func(context.Context) error) ([][]float32, error) {
	if err := validateTableIndex(ctx, validate); err != nil {
		return nil, err
	}
	identity, cacheable := model.(interface{ persistentEmbeddingIdentity() string })
	if s == nil || !cacheable || identity.persistentEmbeddingIdentity() == "" ||
		!p.Workspace.Valid() || !p.User.ID.Valid() || !datasource.Valid() {
		return encodeTableVectorsChecked(ctx, model, tables, validate)
	}
	key, err := s.tableVectorFlightKey(p, datasource, identity.persistentEmbeddingIdentity(), tables)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		flight := s.tableVectorFlights.DoChan(key, func() (any, error) {
			return s.indexedTableVectorsUnshared(ctx, p, datasource, model, tables, identity.persistentEmbeddingIdentity(), validate)
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-flight:
			if result.Err != nil {
				if attempt == 0 && ctx.Err() == nil &&
					(errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded)) {
					// The caller that owned the shared build left; this still-live
					// request may start a new flight under its own context.
					continue
				}
				return nil, result.Err
			}
			vectors, ok := result.Val.([][]float32)
			if !ok || len(vectors) != len(tables) {
				return nil, ErrUnavailable
			}
			if err := validateTableIndex(ctx, validate); err != nil {
				return nil, err
			}
			return vectors, nil
		}
	}
	return nil, ErrUnavailable
}

// The flight key binds the current caller and the ordered authorized schema.
// Its HMAC does not expose schema text in the process's shared-flight map.
func (s *Store) tableVectorFlightKey(p Principal, datasource ID, modelIdentity string,
	tables []modelTableCandidate) (string, error) {
	mac := hmac.New(sha256.New, s.indexKey)
	_, _ = mac.Write([]byte("agentsql:chatbi:table-vector-flight:v1\x00"))
	input := struct {
		Workspace, User, EmbeddedApp, VisitorScope, Datasource, Model string
		Tables                                                        []struct{ Key, Text string }
	}{Workspace: string(p.Workspace), User: string(p.User.ID), EmbeddedApp: string(p.EmbeddedApp),
		VisitorScope: p.VisitorScope, Datasource: string(datasource), Model: modelIdentity}
	input.Tables = make([]struct{ Key, Text string }, len(tables))
	for index, table := range tables {
		input.Tables[index].Key, input.Tables[index].Text = table.key, table.text
	}
	if err := json.NewEncoder(mac).Encode(input); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *Store) indexedTableVectorsUnshared(ctx context.Context, p Principal, datasource ID,
	model EmbeddingModel, tables []modelTableCandidate, modelIdentity string,
	validate func(context.Context) error) ([][]float32, error) {
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
		fingerprints[index] = s.tableVectorFingerprint(modelIdentity, table.text)
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
	vectors, err := encodeTableVectorsChecked(ctx, model, toEncode, validate)
	if err != nil {
		return nil, err
	}
	writes := make([]vectorIndexWrite, 0, len(missing))
	for offset, index := range missing {
		result[index] = vectors[offset]
		writes = append(writes, vectorIndexWrite{keys[index], revisions[index], fingerprints[index], vectors[offset]})
	}
	if err := validateTableIndex(ctx, validate); err != nil {
		return nil, err
	}
	if err := s.putVectorBatch(ctx, writes); err != nil {
		return nil, err
	}
	return result, nil
}
