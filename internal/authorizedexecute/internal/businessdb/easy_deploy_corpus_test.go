package businessdb

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

type easyDeployS6Corpus struct {
	Schema  string                    `json:"schema"`
	Entries []easyDeployS6CorpusEntry `json:"entries"`
}

type easyDeployS6CorpusEntry struct {
	ID                 string                   `json:"id"`
	Tier               string                   `json:"tier"`
	SQL                string                   `json:"sql"`
	SyntaxExpectation  ClosedRequestDisposition `json:"syntax_expectation"`
	Expectation        ClosedRequestDisposition `json:"expectation"`
	EligibleDirectCRUD bool                     `json:"eligible_direct_crud"`
	NativeSupported    bool                     `json:"native_supported"`
	Gate               string                   `json:"gate"`
	Note               string                   `json:"note"`
}

func TestEasyDeployS6CustomerCorpusClassificationAndCoverage(t *testing.T) {
	t.Parallel()
	corpus := loadEasyDeployS6Corpus(t)
	require.Equal(t, "agentsql.easy-deploy-s6-corpus.v1", corpus.Schema)
	require.Len(t, corpus.Entries, 74)

	seen := make(map[string]struct{}, len(corpus.Entries))
	observations := make([]EasyDeployCorpusObservation, 0, len(corpus.Entries))
	for _, entry := range corpus.Entries {
		entry := entry
		t.Run(entry.ID, func(t *testing.T) {
			require.NotEmpty(t, entry.ID)
			require.NotEmpty(t, entry.Tier)
			require.NotEmpty(t, entry.SQL)
			require.NotEmpty(t, entry.Gate)
			require.NotEmpty(t, entry.Note)
			_, duplicate := seen[entry.ID]
			require.False(t, duplicate, "duplicate corpus id")
			seen[entry.ID] = struct{}{}

			_, disposition, _ := ClassifyEasyDeployClosedSyntax(entry.SQL)
			require.Equal(t, entry.SyntaxExpectation, disposition,
				"raw parser classification drift; final expectation=%s gate=%s", entry.Expectation, entry.Gate)
			if entry.EligibleDirectCRUD {
				require.Equal(t, "direct_crud", entry.Tier)
				require.Contains(t, []ClosedRequestDisposition{ClosedRequestProven, ClosedRequestMustReject}, entry.Expectation)
			}
			if entry.Expectation == ClosedRequestProven {
				require.True(t, entry.NativeSupported, "native must cover every closed allow")
			}
		})
		observations = append(observations, EasyDeployCorpusObservation{
			ID:                 entry.ID,
			Tier:               entry.Tier,
			Expectation:        entry.Expectation,
			EligibleDirectCRUD: entry.EligibleDirectCRUD,
			ClosedSupported:    entry.Expectation == ClosedRequestProven,
			NativeSupported:    entry.NativeSupported,
		})
	}

	report := BuildEasyDeployCorpusReport(observations)
	require.Equal(t, map[string]int{
		"direct_crud": 24, "reporting": 12, "view_cte_lateral": 10, "dml": 14, "negative_escape": 14,
	}, report.ByTier)
	require.Equal(t, 32, report.ClosedCovered)
	require.InDelta(t, 43.24, report.ClosedCoveragePercent, 0.01)
	require.Equal(t, 46, report.NativeCovered)
	require.InDelta(t, 62.16, report.NativeCoveragePercent, 0.01)
	require.Zero(t, report.Divergences)
	require.Equal(t, 24, report.EligibleDirectCRUD)
	require.Equal(t, 1, report.EligibleDirectCRUDRejected)
	require.InDelta(t, 4.17, report.EligibleFalseRejectPercent, 0.01)
}

func loadEasyDeployS6Corpus(t testing.TB) easyDeployS6Corpus {
	t.Helper()
	data, err := os.ReadFile("testdata/easy_deploy_s6_customer_corpus.json")
	require.NoError(t, err)
	var corpus easyDeployS6Corpus
	require.NoError(t, json.Unmarshal(data, &corpus))
	return corpus
}
