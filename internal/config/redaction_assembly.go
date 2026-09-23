package config

import (
	"fmt"

	"github.com/cuipengdba/agentsql/internal/mask"
)

type RedactionAssembly struct {
	Plan        mask.RedactionPlan
	PlanOptions []mask.Option
	Verify      func(version int, value, candidate string) (bool, error)
}

func BuildRedactionAssembly(rc RedactionConfig) (RedactionAssembly, error) {
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
		Verify: func(version int, value, candidate string) (bool, error) {
			if version != 1 {
				return false, fmt.Errorf("hash version %d is not supported by this assembly", version)
			}
			return mask.VerifyFingerprint(version, verifyKey, value, candidate)
		},
	}, nil
}
