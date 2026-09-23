package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/cuipengdba/agentsql/internal/audit"
	"github.com/cuipengdba/agentsql/internal/discovery"
	"github.com/cuipengdba/agentsql/internal/mask"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/google/uuid"
)

const (
	maxDiscoveryApplyItems     = 50
	maxExternalIdentifierRunes = 128
)

type discoverAuditDetails struct {
	CanonicalTables    []string `json:"canonical_tables"`
	Sampling           bool     `json:"sampling"`
	SampleRows         int      `json:"sample_rows"`
	TablesRequested    int      `json:"tables_requested"`
	TablesScanned      int      `json:"tables_scanned"`
	ColumnsSeen        int      `json:"columns_seen"`
	CandidateColumns   int      `json:"candidate_columns"`
	SampledColumns     int      `json:"sampled_columns"`
	SampledValuesCount int      `json:"sampled_values_count"`
	FindingsCount      int      `json:"findings_count"`
	DurationMS         int64    `json:"duration_ms"`
	ErrorCode          string   `json:"error_code,omitempty"`
}

type applyAuditDetails struct {
	OperationID     string                  `json:"operation_id"`
	DatasourceID    string                  `json:"datasource_id"`
	Requested       int                     `json:"requested"`
	Created         int                     `json:"created"`
	Existing        int                     `json:"existing"`
	CoveredByGlobal int                     `json:"covered_by_global"`
	Conflict        int                     `json:"conflict"`
	Ambiguous       int                     `json:"ambiguous"`
	ColumnKeys      []string                `json:"column_keys"`
	Rules           []applyAuditRuleDetails `json:"rules"`
}

type applyAuditRuleDetails struct {
	Table         string               `json:"table"`
	Column        string               `json:"column"`
	SensitiveType string               `json:"sensitive_type"`
	Algo          string               `json:"algo"`
	Range         *discoveryApplyRange `json:"range,omitempty"`
}

func (handler *Handler) datasourcesDiscover(writer http.ResponseWriter, request *http.Request) {
	var input discoveryInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.discoveryDecodeFailure(writer, err)
		return
	}
	datasourceID := request.PathValue("id")
	if _, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), datasourceID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handler.notFound(writer)
			return
		}
		handler.internal(writer, err)
		return
	}
	scanRequest, validationCode := discoveryScanRequest(input)
	if validationCode != "" {
		handler.discoveryFailure(writer, http.StatusUnprocessableEntity, validationCode, "发现范围或参数无效")
		return
	}
	started := time.Now()
	if handler.deps.Discovery == nil {
		handler.discoveryFailure(writer, http.StatusServiceUnavailable, "DISCOVERY_UNAVAILABLE", "敏感发现服务不可用")
		return
	}
	result, scanErr := handler.deps.Discovery.Discover(request.Context(), handler.adminUser, datasourceID, scanRequest)
	errorCode := discoveryErrorCode(scanErr)
	details := discoverDetails(scanRequest, result, time.Since(started), errorCode)
	if err := handler.recordDiscoverAudit(request, datasourceID, details, scanErr == nil); err != nil {
		handler.discoveryFailure(writer, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "审计服务不可用")
		return
	}
	if scanErr != nil {
		handler.writeDiscoveryError(writer, errorCode)
		return
	}
	handler.ok(writer, result)
}

func discoveryScanRequest(input discoveryInput) (discovery.ScanRequest, string) {
	if len(input.Tables) < discovery.MinTables || len(input.Tables) > discovery.MaxTables {
		return discovery.ScanRequest{}, "DISCOVERY_SCOPE_LIMIT"
	}
	tables := make([]discovery.TableRef, len(input.Tables))
	seen := make(map[discovery.TableRef]struct{}, len(input.Tables))
	for index, item := range input.Tables {
		if !safeExternalIdentifier(item.Schema) || !safeExternalIdentifier(item.Table) {
			return discovery.ScanRequest{}, "DISCOVERY_INVALID_IDENTIFIER"
		}
		tables[index] = discovery.TableRef{Schema: item.Schema, Table: item.Table}
		if _, duplicate := seen[tables[index]]; duplicate {
			return discovery.ScanRequest{}, "DISCOVERY_INVALID_REQUEST"
		}
		seen[tables[index]] = struct{}{}
	}
	sampleRows := 0
	if input.SampleRows != nil {
		sampleRows = *input.SampleRows
		if sampleRows < 1 || sampleRows > discovery.MaxSampleRows {
			return discovery.ScanRequest{}, "DISCOVERY_SCOPE_LIMIT"
		}
	}
	if input.Sampling != nil && !*input.Sampling && input.SampleRows != nil {
		return discovery.ScanRequest{}, "DISCOVERY_INVALID_REQUEST"
	}
	categories := make([]discovery.Category, len(input.Categories))
	for index, category := range input.Categories {
		normalized := discovery.Category(strings.ToLower(strings.TrimSpace(category)))
		if !discovery.IsKnownCategory(normalized) {
			return discovery.ScanRequest{}, "DISCOVERY_UNKNOWN_CATEGORY"
		}
		categories[index] = normalized
	}
	return discovery.ScanRequest{Tables: tables, Sampling: input.Sampling, SampleRows: sampleRows, Categories: categories}, ""
}

func safeExternalIdentifier(value string) bool {
	return value != "" && safeOptionalExternalIdentifier(value)
}

func safeOptionalExternalIdentifier(value string) bool {
	if value != strings.TrimSpace(value) || len([]rune(value)) > maxExternalIdentifierRunes {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func safeMaskScopeIdentifier(value string) bool {
	return safeOptionalExternalIdentifier(value) && !strings.ContainsAny(value, "*.%")
}

func discoverDetails(request discovery.ScanRequest, result discovery.ScanResult, duration time.Duration, errorCode string) discoverAuditDetails {
	sampling := true
	if request.Sampling != nil {
		sampling = *request.Sampling
	}
	sampleRows := request.SampleRows
	if sampling && sampleRows == 0 {
		sampleRows = discovery.DefaultSampleRows
	}
	tables := make([]string, len(request.Tables))
	for index, table := range request.Tables {
		tables[index] = table.Schema + "." + table.Table
	}
	sort.Strings(tables)
	stats := result.Stats
	return discoverAuditDetails{CanonicalTables: tables, Sampling: sampling, SampleRows: sampleRows,
		TablesRequested: len(request.Tables), TablesScanned: stats.TablesScanned, ColumnsSeen: stats.ColumnsSeen,
		CandidateColumns: stats.CandidateColumns, SampledColumns: stats.SampledColumns,
		SampledValuesCount: stats.SampledValuesCount, FindingsCount: stats.FindingsCount,
		DurationMS: duration.Milliseconds(), ErrorCode: errorCode}
}

func (handler *Handler) recordDiscoverAudit(request *http.Request, datasourceID string, details discoverAuditDetails, success bool) error {
	if handler.deps.Runtime.ManagementAudit == nil {
		return errors.New("management audit unavailable")
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	action, actorType, actorID := audit.ActionDiscover, "admin", handler.adminUser
	datasource := datasourceID
	decision := "error"
	if success {
		decision = "allow"
	}
	log := model.AuditLog{DatasourceID: &datasource, Decision: decision, Action: &action, ActorType: &actorType, ActorID: &actorID}
	text := string(encoded)
	log.DetailsJSON = &text
	if details.ErrorCode != "" {
		code := details.ErrorCode
		log.ErrorMsg = &code
	}
	_, err = handler.deps.Runtime.ManagementAudit.Record(request.Context(), log)
	return err
}

func discoveryErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, store.ErrNotFound) {
		return "DATASOURCE_NOT_FOUND"
	}
	var classified *discovery.ClassifiedError
	if errors.As(err, &classified) {
		return string(classified.Code)
	}
	return "DISCOVERY_INTERNAL"
}

func (handler *Handler) writeDiscoveryError(writer http.ResponseWriter, code string) {
	switch code {
	case "DATASOURCE_NOT_FOUND":
		handler.notFound(writer)
	case string(discovery.CodePermissionDenied):
		handler.discoveryFailure(writer, http.StatusForbidden, code, "数据源账号无权读取请求范围")
	case string(discovery.CodeScopeNotVisible):
		handler.discoveryFailure(writer, http.StatusForbidden, code, "请求范围不可见")
	case string(discovery.CodeRateLimited):
		handler.discoveryFailure(writer, http.StatusTooManyRequests, code, "敏感发现请求过于频繁")
	case string(discovery.CodeTimeout):
		handler.discoveryFailure(writer, http.StatusGatewayTimeout, code, "敏感发现超时")
	case string(discovery.CodeInvalidRequest), string(discovery.CodeScopeLimit), string(discovery.CodeCandidateLimit), string(discovery.CodeSampleLimit), string(discovery.CodeUnknownCategory):
		handler.discoveryFailure(writer, http.StatusUnprocessableEntity, code, "发现范围或参数无效")
	default:
		handler.discoveryFailure(writer, http.StatusInternalServerError, "DISCOVERY_INTERNAL", "敏感发现失败")
	}
}

func (handler *Handler) discoveryDecodeFailure(writer http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		handler.discoveryFailure(writer, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "请求体超过 1MiB")
		return
	}
	handler.discoveryFailure(writer, http.StatusBadRequest, "INVALID_JSON", "请求体无效")
}

func (handler *Handler) discoveryFailure(writer http.ResponseWriter, status int, code, message string) {
	handler.write(writer, status, status, message, struct {
		ErrorCode string `json:"error_code"`
	}{ErrorCode: code})
}

type canonicalApplyGroup struct {
	item discoveryApplyItemView
	key  string
}

func (handler *Handler) datasourcesDiscoverApply(writer http.ResponseWriter, request *http.Request) {
	var input discoveryApplyInput
	if err := decodeJSON(writer, request, &input); err != nil {
		handler.discoveryDecodeFailure(writer, err)
		return
	}
	if len(input.Items) < 1 || len(input.Items) > maxDiscoveryApplyItems {
		handler.discoveryFailure(writer, http.StatusUnprocessableEntity, "DISCOVERY_SCOPE_LIMIT", "apply 项目数量必须为 1–50")
		return
	}
	datasourceID := request.PathValue("id")
	if _, err := handler.deps.Runtime.Store.Datasources().Get(request.Context(), datasourceID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			handler.notFound(writer)
			return
		}
		handler.internal(writer, err)
		return
	}
	groups, ambiguous, code := normalizeApplyItems(input.Items)
	if code != "" {
		handler.discoveryFailure(writer, http.StatusUnprocessableEntity, code, "不可生成该类别的脱敏草稿")
		return
	}
	drafts := make([]store.DiscoveryDraft, 0, len(groups))
	keys := make([]string, 0, len(groups)+len(ambiguous))
	for _, group := range groups {
		drafts = append(drafts, discoveryDraftFromItem(uuid.NewString(), group.item))
		keys = append(keys, group.key)
	}
	for _, item := range ambiguous {
		keys = append(keys, item.Table+"."+strings.ToLower(item.Column))
	}
	sort.Strings(keys)
	operationID := uuid.NewString()
	auditRules := applyAuditRules(groups)
	if len(drafts) == 0 {
		response := discoveryApplyResponse{Created: []discoveryApplyItemView{}, Existing: []discoveryApplyItemView{}, CoveredByGlobal: []discoveryApplyItemView{}, Conflicts: []discoveryApplyItemView{}, Ambiguous: ambiguous,
			Counts: discoveryApplyCounts{Requested: len(input.Items), Ambiguous: len(ambiguous)}}
		if err := handler.recordApplyAudit(request, datasourceID, applyAuditDetails{OperationID: operationID, DatasourceID: datasourceID, Requested: len(input.Items), Ambiguous: len(ambiguous), ColumnKeys: keys, Rules: auditRules}); err != nil {
			handler.discoveryFailure(writer, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "审计服务不可用")
			return
		}
		handler.ok(writer, response)
		return
	}
	repository := handler.deps.Runtime.Store.MaskRules()
	outcome, recorded, err := repository.ApplyDiscoveryDraftsWithAudit(request.Context(), datasourceID, drafts, func(outcome store.DiscoveryApplyOutcome) (model.AuditLog, error) {
		details := applyAuditDetails{OperationID: operationID, DatasourceID: datasourceID, Requested: len(input.Items), Created: len(outcome.Created), Existing: len(outcome.Existing), CoveredByGlobal: len(outcome.CoveredByGlobal), Conflict: len(outcome.Conflicts), Ambiguous: len(ambiguous), ColumnKeys: keys, Rules: auditRules}
		return handler.applyAuditLog(datasourceID, details)
	})
	if err != nil {
		handler.writeDiscoveryApplyError(writer, err)
		return
	}
	if recorded.ID != 0 {
		handler.deps.Runtime.PublishManagementAudit(recorded)
	}
	response := applyResponse(groups, ambiguous, outcome, len(input.Items))
	if len(outcome.Conflicts) != 0 {
		handler.write(writer, http.StatusConflict, http.StatusConflict, "同名列已存在不同脱敏规则", response)
		return
	}
	handler.ok(writer, response)
}

func (handler *Handler) writeDiscoveryApplyError(writer http.ResponseWriter, err error) {
	if store.IsMaskRuleConflict(err) {
		handler.discoveryFailure(writer, http.StatusConflict, "MASK_RULE_CONFLICT", "同名列已存在不同脱敏规则")
		return
	}
	if store.IsInvalidDiscoveryDraft(err) {
		handler.discoveryFailure(writer, http.StatusUnprocessableEntity, "DISCOVERY_NOT_APPLICABLE", "脱敏草稿不适用于发现流程")
		return
	}
	handler.discoveryFailure(writer, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "审计或草稿持久化不可用")
}

func normalizeApplyItems(items []discoveryApplyItemInput) ([]canonicalApplyGroup, []discoveryApplyItemView, string) {
	type aggregate struct {
		group     canonicalApplyGroup
		ambiguous bool
	}
	byPhysicalKey := make(map[string]*aggregate)
	order := make([]string, 0)
	for _, raw := range items {
		if !safeOptionalExternalIdentifier(raw.Schema) || !safeMaskScopeIdentifier(raw.Table) || raw.Table == "" || !safeExternalIdentifier(raw.Column) {
			return nil, nil, "DISCOVERY_INVALID_IDENTIFIER"
		}
		category := discovery.Category(strings.ToLower(strings.TrimSpace(raw.Category)))
		sensitiveType := mask.SensitiveType(strings.ToLower(strings.TrimSpace(raw.SensitiveType)))
		algo := mask.Algorithm(strings.ToLower(strings.TrimSpace(raw.Algo)))
		rule := discovery.RecommendedRule{SensitiveType: sensitiveType, Algo: algo}
		if raw.Range != nil {
			rule.Range = &discovery.RangeHint{
				BucketWidth:  copyDiscoveryInt64(raw.Range.BucketWidth),
				BucketOffset: copyDiscoveryInt64(raw.Range.BucketOffset),
				Granularity:  strings.ToLower(strings.TrimSpace(raw.Range.Granularity)),
			}
		}
		canonical, canonicalErr := discovery.CanonicalizeRule(category, rule)
		if sensitiveType != mask.SensitiveType(category) || canonicalErr != nil || discovery.ValidateApplicable(category, canonical) != nil {
			return nil, nil, "DISCOVERY_NOT_APPLICABLE"
		}
		column := mask.NormalizeColumnName(raw.Column)
		if column == "" {
			return nil, nil, "DISCOVERY_INVALID_IDENTIFIER"
		}
		view := discoveryApplyItemView{Schema: "", Table: raw.Table, Column: column, Category: string(category), SensitiveType: string(canonical.SensitiveType), Algo: string(canonical.Algo), Range: discoveryRangeFromHint(canonical.Range)}
		physicalKey := raw.Table + "\x00" + column
		current := byPhysicalKey[physicalKey]
		if current == nil {
			byPhysicalKey[physicalKey] = &aggregate{group: canonicalApplyGroup{item: view, key: raw.Table + "." + column}}
			order = append(order, physicalKey)
			continue
		}
		if !sameDiscoveryApplyRule(current.group.item, view) {
			current.ambiguous = true
		}
	}
	groups := make([]canonicalApplyGroup, 0, len(order))
	ambiguous := make([]discoveryApplyItemView, 0)
	for _, physicalKey := range order {
		value := byPhysicalKey[physicalKey]
		if value.ambiguous {
			ambiguous = append(ambiguous, value.group.item)
		} else {
			groups = append(groups, value.group)
		}
	}
	return groups, ambiguous, ""
}

func applyResponse(groups []canonicalApplyGroup, ambiguous []discoveryApplyItemView, outcome store.DiscoveryApplyOutcome, requested int) discoveryApplyResponse {
	byPhysicalKey := make(map[string]discoveryApplyItemView, len(groups))
	byColumn := make(map[string][]discoveryApplyItemView, len(groups))
	for _, group := range groups {
		physicalKey := group.item.Table + "\x00" + group.item.Column
		byPhysicalKey[physicalKey] = group.item
		byColumn[group.item.Column] = append(byColumn[group.item.Column], group.item)
	}
	convert := func(rules []model.MaskRule, coveredByGlobal bool) []discoveryApplyItemView {
		views := make([]discoveryApplyItemView, 0, len(rules))
		columnOffsets := make(map[string]int)
		for _, rule := range rules {
			column := mask.NormalizeColumnName(rule.ColumnName)
			view := byPhysicalKey[rule.TableName+"\x00"+column]
			if coveredByGlobal {
				candidates := byColumn[column]
				offset := columnOffsets[column]
				if offset < len(candidates) {
					view = candidates[offset]
					columnOffsets[column] = offset + 1
				}
			}
			view.Column, view.SensitiveType, view.Algo, view.RuleID = mask.NormalizeColumnName(rule.ColumnName), rule.SensitiveType, rule.Algo, rule.ID
			view.Range = discoveryRangeFromModel(rule)
			views = append(views, view)
		}
		return views
	}
	return discoveryApplyResponse{Created: convert(outcome.Created, false), Existing: convert(outcome.Existing, false), CoveredByGlobal: convert(outcome.CoveredByGlobal, true), Conflicts: convert(outcome.Conflicts, false), Ambiguous: ambiguous,
		Counts: discoveryApplyCounts{Requested: requested, Created: len(outcome.Created), Existing: len(outcome.Existing), CoveredByGlobal: len(outcome.CoveredByGlobal), Conflicts: len(outcome.Conflicts), Ambiguous: len(ambiguous)}}
}

func sameDiscoveryApplyRule(left, right discoveryApplyItemView) bool {
	if left.Category != right.Category || left.SensitiveType != right.SensitiveType || left.Algo != right.Algo {
		return false
	}
	if left.Range == nil || right.Range == nil {
		return left.Range == nil && right.Range == nil
	}
	return sameDiscoveryInt64(left.Range.BucketWidth, right.Range.BucketWidth) &&
		sameDiscoveryInt64(left.Range.BucketOffset, right.Range.BucketOffset) &&
		left.Range.Granularity == right.Range.Granularity
}

func sameDiscoveryInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func copyDiscoveryInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func discoveryRangeFromHint(hint *discovery.RangeHint) *discoveryApplyRange {
	if hint == nil {
		return nil
	}
	return &discoveryApplyRange{
		BucketWidth:  copyDiscoveryInt64(hint.BucketWidth),
		BucketOffset: copyDiscoveryInt64(hint.BucketOffset),
		Granularity:  hint.Granularity,
	}
}

func discoveryRangeFromModel(rule model.MaskRule) *discoveryApplyRange {
	if rule.RangeBucketWidth == nil && rule.RangeBucketOffset == nil && rule.RangeGranularity == nil {
		return nil
	}
	rangeView := &discoveryApplyRange{
		BucketWidth:  copyDiscoveryInt64(rule.RangeBucketWidth),
		BucketOffset: copyDiscoveryInt64(rule.RangeBucketOffset),
	}
	if rule.RangeGranularity != nil {
		rangeView.Granularity = *rule.RangeGranularity
	}
	return rangeView
}

func applyAuditRules(groups []canonicalApplyGroup) []applyAuditRuleDetails {
	rules := make([]applyAuditRuleDetails, 0, len(groups))
	for _, group := range groups {
		rules = append(rules, applyAuditRuleDetails{
			Table: group.item.Table, Column: group.item.Column,
			SensitiveType: group.item.SensitiveType, Algo: group.item.Algo,
			Range: group.item.Range,
		})
	}
	return rules
}

func discoveryDraftFromItem(id string, item discoveryApplyItemView) store.DiscoveryDraft {
	draft := store.DiscoveryDraft{
		ID: id, SchemaName: "", TableName: item.Table, ColumnName: item.Column,
		SensitiveType: item.SensitiveType, Algo: item.Algo,
	}
	if item.Range != nil {
		draft.RangeBucketWidth = copyDiscoveryInt64(item.Range.BucketWidth)
		draft.RangeBucketOffset = copyDiscoveryInt64(item.Range.BucketOffset)
		draft.RangeGranularity = item.Range.Granularity
	}
	return draft
}

func (handler *Handler) applyAuditLog(datasourceID string, details applyAuditDetails) (model.AuditLog, error) {
	encoded, err := json.Marshal(details)
	if err != nil {
		return model.AuditLog{}, err
	}
	action, actorType, actorID, datasource := audit.ActionDiscoverApply, "admin", handler.adminUser, datasourceID
	text := string(encoded)
	decision := "allow"
	if details.Conflict != 0 {
		decision = "error"
	}
	return model.AuditLog{DatasourceID: &datasource, Decision: decision, Action: &action, ActorType: &actorType, ActorID: &actorID, DetailsJSON: &text}, nil
}

func (handler *Handler) recordApplyAudit(request *http.Request, datasourceID string, details applyAuditDetails) error {
	if handler.deps.Runtime.ManagementAudit == nil {
		return errors.New("management audit unavailable")
	}
	log, err := handler.applyAuditLog(datasourceID, details)
	if err != nil {
		return err
	}
	_, err = handler.deps.Runtime.ManagementAudit.Record(request.Context(), log)
	return err
}
