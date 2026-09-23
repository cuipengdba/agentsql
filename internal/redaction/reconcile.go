// Package redaction owns process-observed key metadata and registry drift
// classification. It never accepts or stores key material.
package redaction

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/cuipengdba/agentsql/internal/model"
)

const (
	DriftActiveNotRegistered          = 3
	DriftConfiguredCommitmentMismatch = 4
)

var (
	ErrActiveCommitmentMismatch = errors.New("active redaction key commitment mismatch")
	ErrRetiredKeyIDReuse        = errors.New("retired redaction key version is configured again")
	ErrInvalidRegistryState     = errors.New("redaction key registry contains an invalid state")
)

// Key is one observed version. Commitment is safe to expose; key material is
// deliberately absent from this package's type system.
type Key struct {
	ID         int    `json:"id"`
	Commitment string `json:"commitment"`
}

// Observed is the immutable, non-secret projection of the process keyring.
type Observed struct {
	Status        string `json:"status"`
	Mode          string `json:"mode,omitempty"`
	ActiveVersion int    `json:"active_version,omitempty"`
	Keys          []Key  `json:"keys,omitempty"`
	Revision      string `json:"revision,omitempty"`
}

// Detail is one bounded drift classification. Message is operational metadata
// only and must never contain key material.
type Detail struct {
	Number  int    `json:"number"`
	Kind    string `json:"kind"`
	Version int    `json:"version,omitempty"`
	Message string `json:"message"`
}

// Result is one newly computed reconciliation snapshot.
type Result struct {
	Ready       bool     `json:"ready"`
	Unsatisfied []int    `json:"unsatisfied,omitempty"`
	Observed    Observed `json:"observed"`
	Warnings    []Detail `json:"warnings"`
	Information []Detail `json:"information"`
}

// Reconcile applies the normative startup order #2, #3/#4, then #5/#6/#7.
// Registry read failures (#1) are handled by the caller because this function
// is intentionally pure.
func Reconcile(observed Observed, registered []model.RedactionKeyVersion) (Result, error) {
	result := Result{
		Ready:       true,
		Observed:    CloneObserved(observed),
		Warnings:    make([]Detail, 0),
		Information: make([]Detail, 0),
	}
	if observed.Status != "available" {
		return result, nil
	}

	byID := make(map[int]model.RedactionKeyVersion, len(registered))
	activeRegistryIDs := make([]int, 0, 1)
	for _, version := range registered {
		id, err := strconv.Atoi(version.ID)
		if err != nil || id < 1 || id > 9999 || strconv.Itoa(id) != version.ID {
			return Result{}, fmt.Errorf("registry key id is invalid: %w", ErrInvalidRegistryState)
		}
		switch version.State {
		case model.RedactionKeyStateStandby, model.RedactionKeyStateActive, model.RedactionKeyStateLegacy, model.RedactionKeyStateRetired:
		default:
			return Result{}, fmt.Errorf("registry key version %d has invalid state: %w", id, ErrInvalidRegistryState)
		}
		if _, duplicate := byID[id]; duplicate {
			return Result{}, fmt.Errorf("registry key version %d is duplicated: %w", id, ErrInvalidRegistryState)
		}
		byID[id] = version
		if version.State == model.RedactionKeyStateActive {
			activeRegistryIDs = append(activeRegistryIDs, id)
		}
	}
	if len(activeRegistryIDs) > 1 {
		return Result{}, fmt.Errorf("registry has multiple active redaction key versions: %w", ErrInvalidRegistryState)
	}

	observedByID := make(map[int]string, len(observed.Keys))
	for _, key := range observed.Keys {
		observedByID[key.ID] = key.Commitment
	}

	// #2 is checked before any readiness-only category.
	if registeredActive, exists := byID[observed.ActiveVersion]; exists && registeredActive.Commitment != observedByID[observed.ActiveVersion] {
		return Result{}, fmt.Errorf("redaction key version %d: %w", observed.ActiveVersion, ErrActiveCommitmentMismatch)
	}
	for _, key := range observed.Keys {
		if registeredKey, exists := byID[key.ID]; exists && registeredKey.State == model.RedactionKeyStateRetired {
			return Result{}, fmt.Errorf("redaction key version %d: %w", key.ID, ErrRetiredKeyIDReuse)
		}
	}

	// #3 then #4 determine readiness. Details are deterministic by version.
	activeRegistered := false
	if registeredActive, exists := byID[observed.ActiveVersion]; exists {
		activeRegistered = registeredActive.State == model.RedactionKeyStateActive
	}
	if !activeRegistered {
		result.Ready = false
		result.Unsatisfied = append(result.Unsatisfied, DriftActiveNotRegistered)
		result.Warnings = append(result.Warnings, Detail{
			Number: 3, Kind: "active_not_registered", Version: observed.ActiveVersion,
			Message: fmt.Sprintf("process active redaction key version %d is not registry active", observed.ActiveVersion),
		})
	}
	keys := append([]Key(nil), observed.Keys...)
	sort.Slice(keys, func(i, j int) bool { return keys[i].ID < keys[j].ID })
	for _, key := range keys {
		if key.ID == observed.ActiveVersion {
			continue
		}
		if registeredKey, exists := byID[key.ID]; exists && registeredKey.Commitment != key.Commitment {
			result.Ready = false
			if !containsNumber(result.Unsatisfied, DriftConfiguredCommitmentMismatch) {
				result.Unsatisfied = append(result.Unsatisfied, DriftConfiguredCommitmentMismatch)
			}
			result.Warnings = append(result.Warnings, Detail{
				Number: 4, Kind: "configured_commitment_mismatch", Version: key.ID,
				Message: fmt.Sprintf("configured redaction key version %d differs from registry", key.ID),
			})
		}
	}

	// #5: only non-active configured versions absent from the registry.
	for _, key := range keys {
		if key.ID == observed.ActiveVersion {
			continue
		}
		if _, exists := byID[key.ID]; !exists {
			result.Warnings = append(result.Warnings, Detail{
				Number: 5, Kind: "standby_unregistered", Version: key.ID,
				Message: fmt.Sprintf("standby redaction key version %d is not registered", key.ID),
			})
		}
	}

	// #6: registry history outside this process manifest is informational.
	registeredIDs := make([]int, 0, len(byID))
	for id := range byID {
		registeredIDs = append(registeredIDs, id)
	}
	sort.Ints(registeredIDs)
	for _, id := range registeredIDs {
		if _, exists := observedByID[id]; !exists {
			result.Information = append(result.Information, Detail{
				Number: 6, Kind: "extra_registered", Version: id,
				Message: fmt.Sprintf("registry contains historical redaction key version %d", id),
			})
		}
	}

	// #7 is meaningful only after the active version and commitment agree.
	if registeredActive, exists := byID[observed.ActiveVersion]; exists &&
		registeredActive.State == model.RedactionKeyStateActive &&
		registeredActive.Commitment == observedByID[observed.ActiveVersion] &&
		registeredActive.ConfigRevision != observed.Revision {
		result.Warnings = append(result.Warnings, Detail{
			Number: 7, Kind: "revision_mismatch", Version: observed.ActiveVersion,
			Message: "process and registry redaction key revisions differ",
		})
	}

	sort.Ints(result.Unsatisfied)
	return result, nil
}

func CloneObserved(observed Observed) Observed {
	cloned := observed
	cloned.Keys = append([]Key(nil), observed.Keys...)
	return cloned
}

func CloneResult(result Result) Result {
	cloned := result
	cloned.Observed = CloneObserved(result.Observed)
	cloned.Unsatisfied = append([]int(nil), result.Unsatisfied...)
	cloned.Warnings = append([]Detail(nil), result.Warnings...)
	cloned.Information = append([]Detail(nil), result.Information...)
	return cloned
}

func containsNumber(numbers []int, wanted int) bool {
	for _, number := range numbers {
		if number == wanted {
			return true
		}
	}
	return false
}
