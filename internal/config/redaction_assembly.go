package config

import (
	"fmt"
	"sort"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/redaction"
)

type RedactionAssembly struct {
	Plan        mask.RedactionPlan
	PlanOptions []mask.Option
	Observed    redaction.Observed
	Verify      func(version int, value, candidate string) (bool, error)
}

func BuildRedactionAssembly(rc RedactionConfig) (RedactionAssembly, error) {
	if rc.isMultiKey() {
		return buildMultiKeyRedactionAssembly(rc)
	}
	if rc.HashKeys != nil {
		return RedactionAssembly{}, fmt.Errorf("multi-key redaction configuration must be resolved before assembly")
	}
	if rc.HashKey == "" {
		return RedactionAssembly{}, nil
	}
	key := append([]byte(nil), []byte(rc.HashKey)...)
	plan, err := mask.BuildRedactionPlan(1, map[int][]byte{1: key})
	if err != nil {
		return RedactionAssembly{}, err
	}
	verifyKey := append([]byte(nil), key...)
	return RedactionAssembly{
		Plan:        plan,
		PlanOptions: []mask.Option{mask.WithRedactionPlan(plan)},
		Observed: redaction.Observed{
			Status:        "legacy",
			Mode:          "legacy_single_key_unregistered_commitment",
			ActiveVersion: 1,
		},
		Verify: func(version int, value, candidate string) (bool, error) {
			if version != 1 {
				return false, fmt.Errorf("hash version %d is not supported by this assembly", version)
			}
			return mask.VerifyFingerprint(version, verifyKey, value, candidate)
		},
	}, nil
}

func buildMultiKeyRedactionAssembly(rc RedactionConfig) (RedactionAssembly, error) {
	if rc.HashKeys == nil {
		return RedactionAssembly{}, fmt.Errorf("multi-key redaction manifest is unavailable")
	}
	keys := make(map[int][]byte, len(rc.resolvedKeys))
	commitments := make(map[int]string, len(rc.resolvedKeys))
	ids := make([]int, 0, len(rc.resolvedKeys))
	for id, material := range rc.resolvedKeys {
		keys[id] = append([]byte(nil), material...)
		commitments[id] = mask.KeyCommitment(material)
		ids = append(ids, id)
	}
	plan, err := mask.BuildRedactionPlan(rc.HashKeys.ActiveVersion, keys)
	if err != nil {
		clearKeyMap(keys)
		return RedactionAssembly{}, err
	}
	sort.Ints(ids)
	observed := redaction.Observed{
		Status: "available", Mode: "manifest", ActiveVersion: rc.HashKeys.ActiveVersion,
		Keys: make([]redaction.Key, 0, len(ids)),
	}
	for _, id := range ids {
		observed.Keys = append(observed.Keys, redaction.Key{ID: id, Commitment: commitments[id]})
	}
	if rc.HashKeys.Revision != nil {
		observed.Revision = *rc.HashKeys.Revision
	} else {
		observed.Revision = DeriveRedactionRevision(observed.ActiveVersion, commitments)
	}
	verifyKeys := make(map[int][]byte, len(keys))
	for id, material := range keys {
		verifyKeys[id] = append([]byte(nil), material...)
	}
	clearKeyMap(keys)
	return RedactionAssembly{
		Plan:        plan,
		PlanOptions: []mask.Option{mask.WithRedactionPlan(plan)},
		Observed:    observed,
		Verify: func(version int, value, candidate string) (bool, error) {
			material, exists := verifyKeys[version]
			if !exists {
				return false, nil
			}
			return mask.VerifyFingerprint(version, material, value, candidate)
		},
	}, nil
}

func clearKeyMap(keys map[int][]byte) {
	for id, key := range keys {
		clearBytes(key)
		delete(keys, id)
	}
}
