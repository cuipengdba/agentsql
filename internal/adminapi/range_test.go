package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAdminMaskRuleRangeCRUDAndDefaults(t *testing.T) {
	fixture := newAdminFixture(t)
	status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
		`{"id":"amount-range","datasource_id":"ds-1","table_name":"orders","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10}`)
	require.Equal(t, http.StatusOK, status, body)
	created := decodeMaskRuleResponse(t, body)
	require.NotNil(t, created.RangeBucketWidth)
	require.Equal(t, int64(10), *created.RangeBucketWidth)
	require.NotNil(t, created.RangeBucketOffset, "omitted numeric offset must be normalized to an explicit zero")
	require.Zero(t, *created.RangeBucketOffset)
	require.Nil(t, created.RangeGranularity)

	stored, err := fixture.store.MaskRules().Get(context.Background(), "amount-range")
	require.NoError(t, err)
	require.Equal(t, int64(10), *stored.RangeBucketWidth)
	require.Equal(t, int64(0), *stored.RangeBucketOffset)
	require.Nil(t, stored.RangeGranularity)

	status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
		`{"id":"created-range","datasource_id":"ds-1","table_name":"orders","column_name":"created_at","sensitive_type":"date","algo":"range","range_granularity":"quarter"}`)
	require.Equal(t, http.StatusOK, status, body)
	createdDate := decodeMaskRuleResponse(t, body)
	require.Nil(t, createdDate.RangeBucketWidth)
	require.Nil(t, createdDate.RangeBucketOffset)
	require.NotNil(t, createdDate.RangeGranularity)
	require.Equal(t, "quarter", *createdDate.RangeGranularity)

	status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
		`{"id":"default-date-range","datasource_id":"ds-1","table_name":"orders","column_name":"settled_at","sensitive_type":"date","algo":"range"}`)
	require.Equal(t, http.StatusOK, status, body)
	defaultDate := decodeMaskRuleResponse(t, body)
	require.NotNil(t, defaultDate.RangeGranularity)
	require.Equal(t, "year", *defaultDate.RangeGranularity)

	status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/amount-range", fixture.adminToken,
		`{"datasource_id":"ds-1","table_name":"orders_v2","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":25,"range_bucket_offset":-5}`)
	require.Equal(t, http.StatusOK, status, body)
	updated := decodeMaskRuleResponse(t, body)
	require.Equal(t, int64(25), *updated.RangeBucketWidth)
	require.Equal(t, int64(-5), *updated.RangeBucketOffset)

	status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/amount-range", fixture.adminToken,
		`{"datasource_id":"ds-1","table_name":"orders_v3","column_name":"amount","sensitive_type":"phone","algo":"mask"}`)
	require.Equal(t, http.StatusOK, status, body)
	require.NotContains(t, body, "range_bucket_width")
	require.NotContains(t, body, "range_bucket_offset")
	require.NotContains(t, body, "range_granularity")
	stored, err = fixture.store.MaskRules().Get(context.Background(), "amount-range")
	require.NoError(t, err)
	require.Nil(t, stored.RangeBucketWidth)
	require.Nil(t, stored.RangeBucketOffset)
	require.Nil(t, stored.RangeGranularity)

	status, body = fixture.request(http.MethodGet, "/api/v1/mask_rules?datasource_id=ds-1&page_size=100", fixture.adminToken, "")
	require.Equal(t, http.StatusOK, status, body)
	for _, rule := range decodeMaskRuleListResponse(t, body) {
		if rule.ID == "amount-range" {
			require.Nil(t, rule.RangeBucketWidth)
			require.Nil(t, rule.RangeBucketOffset)
			require.Nil(t, rule.RangeGranularity)
			return
		}
	}
	t.Fatal("updated mask rule was not returned by GET list")
}

func TestAdminMaskRuleRangeValidationAndHashGate(t *testing.T) {
	invalid := []struct {
		name    string
		payload string
	}{
		{name: "number missing width", payload: `{"id":"missing-width","column_name":"amount_1","sensitive_type":"number","algo":"range"}`},
		{name: "date invalid granularity", payload: `{"id":"bad-granularity","column_name":"created_1","sensitive_type":"date","algo":"range","range_granularity":"week"}`},
		{name: "number with granularity", payload: `{"id":"number-granularity","column_name":"amount_2","sensitive_type":"number","algo":"range","range_bucket_width":10,"range_granularity":"year"}`},
		{name: "date with width", payload: `{"id":"date-width","column_name":"created_2","sensitive_type":"date","algo":"range","range_bucket_width":10}`},
		{name: "date with offset", payload: `{"id":"date-offset","column_name":"created_3","sensitive_type":"date","algo":"range","range_bucket_offset":0}`},
		{name: "non range with residual params", payload: `{"id":"mask-residual","column_name":"phone_1","sensitive_type":"phone","algo":"mask","range_bucket_width":10}`},
		{name: "unknown algorithm", payload: `{"id":"unknown-algo","column_name":"phone_2","sensitive_type":"phone","algo":"future"}`},
		{name: "unknown sensitive type", payload: `{"id":"unknown-type","column_name":"future_1","sensitive_type":"future","algo":"block"}`},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAdminFixture(t)
			status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken, test.payload)
			require.Equal(t, http.StatusUnprocessableEntity, status, body)
			require.Contains(t, body, "INVALID_MASK_RULE")
			require.NotContains(t, body, "HASH_REDACTION_UNAVAILABLE")
		})
	}

	t.Run("unknown range field remains a bad request", func(t *testing.T) {
		fixture := newAdminFixture(t)
		status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"typo","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10,"range_bucket_offest":0}`)
		require.Equal(t, http.StatusBadRequest, status, body)
	})

	t.Run("range activation does not require hash key", func(t *testing.T) {
		fixture := newAdminFixture(t)
		status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"disabled-range","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10,"enabled":false}`)
		require.Equal(t, http.StatusOK, status, body)
		status, body = fixture.request(http.MethodPut, "/api/v1/mask_rules/disabled-range", fixture.adminToken,
			`{"column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10,"enabled":true}`)
		require.Equal(t, http.StatusOK, status, body)
		require.NotContains(t, body, "HASH_REDACTION_UNAVAILABLE")

		status, body = fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			`{"id":"enabled-hash","column_name":"name","sensitive_type":"generic","algo":"hash"}`)
		require.Equal(t, http.StatusServiceUnavailable, status, body)
		require.Contains(t, body, "HASH_REDACTION_UNAVAILABLE")
	})
}

func TestRangeParamsFromInputCopiesPointers(t *testing.T) {
	width, offset, granularity := int64(10), int64(0), "month"
	input := maskRuleInput{
		Algo: string(mask.AlgoRange), RangeBucketWidth: &width,
		RangeBucketOffset: &offset, RangeGranularity: &granularity,
	}
	params := rangeParamsFromInput(input)
	require.NotSame(t, input.RangeBucketWidth, params.BucketWidth)
	require.NotSame(t, input.RangeBucketOffset, params.BucketOffset)
	require.NotSame(t, input.RangeGranularity, params.Granularity)
	width, offset, granularity = 20, 5, "year"
	require.Equal(t, int64(10), *params.BucketWidth)
	require.Zero(t, *params.BucketOffset)
	require.Equal(t, "month", string(*params.Granularity))
}

func TestAdminMaskRuleRangeConflictCode(t *testing.T) {
	fixture := newAdminFixture(t)
	for index := 1; index <= 2; index++ {
		status, body := fixture.request(http.MethodPost, "/api/v1/mask_rules", fixture.adminToken,
			fmt.Sprintf(`{"id":"range-conflict-%d","datasource_id":"ds-1","column_name":"amount","sensitive_type":"number","algo":"range","range_bucket_width":10}`, index))
		if index == 1 {
			require.Equal(t, http.StatusOK, status, body)
			continue
		}
		require.Equal(t, http.StatusConflict, status, body)
		require.Contains(t, body, "MASK_RULE_CONFLICT")
	}
	_, err := fixture.store.MaskRules().Get(context.Background(), "range-conflict-2")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func decodeMaskRuleResponse(t *testing.T, body string) maskRuleView {
	t.Helper()
	var response struct {
		Data maskRuleView `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response.Data
}

func decodeMaskRuleListResponse(t *testing.T, body string) []maskRuleView {
	t.Helper()
	var response struct {
		Data struct {
			List []maskRuleView `json:"list"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &response))
	return response.Data.List
}
