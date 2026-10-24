package model

// DatasourceSupportLevel describes the narrowest verified Agent entrypoint,
// not a guarantee that an individual request will be authorized or a claim of
// complete production support.
type DatasourceSupportLevel string

const (
	SupportControlledSQL      DatasourceSupportLevel = "controlled_sql"
	SupportBoundedSQL         DatasourceSupportLevel = "bounded_sql"
	SupportControlledReadTool DatasourceSupportLevel = "controlled_read_tool"
	SupportConnectionPreview  DatasourceSupportLevel = "connection_preview"
)

type DatasourceSupport struct {
	Level       DatasourceSupportLevel `json:"level"`
	AgentTools  []string               `json:"agent_tools"`
	Scope       string                 `json:"scope"`
	EvidenceDoc string                 `json:"evidence_doc"`
}

var datasourceSupport = map[string]DatasourceSupport{
	"postgres": {SupportControlledSQL, []string{"query", "list_schema", "explain_query"},
		"PostgreSQL 14–18 已验证的受控 SQL 子集；PG 兼容产品须按各自版本证据复核，列授权、审批与事务仍受配置和策略约束。", "docs/SPEC.md"},
	"mysql": {SupportControlledSQL, []string{"query", "list_schema", "explain_query"},
		"MySQL 8.0/8.4 受控 SQL 子集；单表 InnoDB 列授权须显式启用。", "docs/v1.3.0-acceptance.md"},
	"vastbase": {SupportBoundedSQL, []string{"query"},
		"Vastbase G100 V3.0 Build 9 PG 模式指定实例的有界只读 SELECT。", "docs/v1.8.0-acceptance.md"},
	"highgo": {SupportBoundedSQL, []string{"query"},
		"HighGo 9.0.10 企业版 PG 模式指定实例的有界只读 SELECT。", "docs/v1.7.0-acceptance.md"},
	"opengauss": {SupportBoundedSQL, []string{"query"},
		"openGauss 7.0.0 LTS 指定实例、严格 TLS 的有界只读 SELECT。", "docs/v2.4.0-acceptance.md"},
	"dm": {SupportBoundedSQL, []string{"query"},
		"达梦 DM8 Pack3 指定实例的有界只读 SELECT；写入和完整方言未开放。", "docs/v1.6.0-acceptance.md"},
	"oracle": {SupportBoundedSQL, []string{"query"},
		"Oracle 26ai Free 23.26.3.0.0 指定单服务的有界只读 SELECT；支持验证 TCPS 与显式启用的 PLAN_TABLE 预检，RAC/mTLS 未验收。", "docs/v3.9.0-acceptance.md"},
	"yashan": {SupportBoundedSQL, []string{"query"},
		"YashanDB 23.4.1.109 指定实例的窄只读 SELECT；TLS 与写入未验收。", "docs/v2.1.0-acceptance.md"},
	"sqlserver": {SupportBoundedSQL, []string{"query"},
		"SQL Server 2025 17.0.5015 Linux 单实例、严格 TLS 与 SHOWPLAN 的受控只读 SELECT；隔离证书轮换和恢复已验证，Always On 未验收。", "docs/v3.9.0-acceptance.md"},
	"mongodb": {SupportControlledReadTool, []string{"find_documents"},
		"MongoDB 7.0.43 单实例与 7.0.45 三成员副本集的严格 TLS 精确集合/字段读取；副本集须批准索引，分片未验收。", "docs/v3.7.0-acceptance.md"},
	"redis": {SupportControlledReadTool, []string{"read_key", "read_hash_fields"},
		"Redis 8.2.9 指定 TLS/ACL 单实例和两数据节点/三 Sentinel 拓扑的精确键与 Hash 字段读取；Cluster 未验收。", "docs/v3.7.0-acceptance.md"},
	"valkey": {SupportControlledReadTool, []string{"read_key", "read_hash_fields"},
		"Valkey 8.1.10 指定 TLS/ACL 单实例和两数据节点/三 Sentinel 拓扑的精确键与 Hash 字段读取；Cluster 未验收。", "docs/v3.7.0-acceptance.md"},
	"clickhouse": {SupportControlledReadTool, []string{"query_analytics"},
		"ClickHouse 26.8.31.2 指定原生 TLS 单节点的有界 MergeTree 查询，可显式收紧读取预算。", "docs/v3.8.0-acceptance.md"},
	"milvus": {SupportControlledReadTool, []string{"search_vectors"},
		"Milvus 2.6.25 指定 TLS 单机的单向量 COSINE 检索，可显式启用权威 Agent 租户范围和获准字段等值过滤。", "docs/v3.8.0-acceptance.md"},
}

// SupportFor returns a defensive copy. A registered type absent from the
// controlled paths remains connection-preview only, including future types.
func SupportFor(dbType string) (DatasourceSupport, bool) {
	if _, ok := TypeSpecFor(dbType); !ok {
		return DatasourceSupport{}, false
	}
	support, ok := datasourceSupport[dbType]
	if !ok {
		support = DatasourceSupport{
			Level:       SupportConnectionPreview,
			AgentTools:  []string{},
			Scope:       "仅连接或管理预览；没有已验收的 Agent 读取工具。",
			EvidenceDoc: "docs/nosql-support.md",
		}
	}
	support.AgentTools = append([]string{}, support.AgentTools...)
	return support, true
}
