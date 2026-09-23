package mask

import "errors"

const versionedActiveEnabled = true

var ErrVersionedActiveNotEnabled = errors.New("versioned active hasher is not enabled in this build")

type RedactionPlan struct {
	active        ActiveHasher
	activeVersion int
}

func (p RedactionPlan) hasActive() bool { return p.active != nil }

func BuildRedactionPlan(activeVersion int, keys map[int][]byte) (RedactionPlan, error) {
	key := append([]byte(nil), keys[activeVersion]...)
	if activeVersion == 1 {
		active, err := newLegacyHasher(key)
		if err != nil {
			return RedactionPlan{}, err
		}
		return RedactionPlan{active: active, activeVersion: activeVersion}, nil
	}
	if activeVersion >= 2 {
		if !versionedActiveEnabled {
			return RedactionPlan{}, ErrVersionedActiveNotEnabled
		}
		active, err := newVersionedHasher(activeVersion, key)
		if err != nil {
			return RedactionPlan{}, err
		}
		return RedactionPlan{active: active, activeVersion: activeVersion}, nil
	}
	return RedactionPlan{}, errors.New("hash version must be between 1 and 9999")
}

func WithRedactionPlan(plan RedactionPlan) Option {
	return func(options *redactorOptions) { options.plan = plan }
}
