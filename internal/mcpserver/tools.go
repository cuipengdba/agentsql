package mcpserver

import (
	"context"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type noInput struct{}

type listSchemaInput struct {
	DatasourceID string `json:"datasource_id" jsonschema:"授权数据源 ID，先调用 list_datasources 获取"`
	Table        string `json:"table,omitempty" jsonschema:"可选表名，只允许 table 或 schema.table"`
}

type sqlInput struct {
	DatasourceID string `json:"datasource_id" jsonschema:"授权数据源 ID"`
	SQL          string `json:"sql" jsonschema:"单条 SQL，不允许堆叠多语句"`
}

type mongoFindInput struct {
	DatasourceID string            `json:"datasource_id" jsonschema:"授权的 MongoDB 数据源 ID"`
	Collection   string            `json:"collection" jsonschema:"指定数据库中的集合名，只允许简单标识符"`
	Fields       []string          `json:"fields" jsonschema:"显式返回的顶层标量字段，1 到 32 个"`
	Filter       map[string]string `json:"filter" jsonschema:"1 到 8 个字符串等值过滤条件，不接受操作符"`
	Limit        int               `json:"limit" jsonschema:"最多返回 1 到 100 条文档"`
}

type keyReadInput struct {
	DatasourceID string `json:"datasource_id" jsonschema:"授权的 Redis 或 Valkey 数据源 ID"`
	Key          string `json:"key" jsonschema:"精确键名，不接受通配符或原生命令"`
}

type analyticsInput struct {
	DatasourceID string            `json:"datasource_id" jsonschema:"授权的 ClickHouse 数据源 ID"`
	Table        string            `json:"table" jsonschema:"当前数据库内已精确授权的 MergeTree 表名"`
	Fields       []string          `json:"fields" jsonschema:"1 到 16 个显式返回列"`
	Filter       map[string]string `json:"filter" jsonschema:"1 到 8 个 String 列的等值条件"`
	Limit        int               `json:"limit" jsonschema:"最多返回 1 到 100 行"`
}

type writeInput struct {
	DatasourceID string `json:"datasource_id" jsonschema:"授权数据源 ID"`
	SQL          string `json:"sql" jsonschema:"单条 INSERT、UPDATE、DELETE 或 DDL"`
	Reason       string `json:"reason" jsonschema:"写操作目的、影响范围和回滚方案，必填"`
}

type approvalInput struct {
	DatasourceID string `json:"datasource_id" jsonschema:"授权数据源 ID"`
	SQL          string `json:"sql" jsonschema:"需要提交人工审批的单条 SQL"`
	Reason       string `json:"reason" jsonschema:"申请审批的原因、影响和回滚方案，必填"`
}

type approvalResultInput struct {
	ApprovalID string `json:"approval_id" jsonschema:"request_approval 返回的审批单号"`
}

// The SDK infers a bare `true` schema for the polymorphic data field and a
// nullable union for optional *bool fields. The actual wire contract omits
// nil values; spelling it explicitly keeps tool schemas portable to clients
// that require schema objects and one type per property.
func portableToolResponseSchema(name string) map[string]any {
	data := map[string]any{"type": "object", "additionalProperties": true}
	if name == "list_datasources" || name == "list_schema" {
		data = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
	}
	properties := map[string]any{
		"decision":                map[string]any{"type": "string"},
		"error_code":              map[string]any{"type": "string"},
		"error_stage":             map[string]any{"type": "string"},
		"reason":                  map[string]any{"type": "string"},
		"suggestion":              map[string]any{"type": "string"},
		"retry_same_request":      map[string]any{"type": "boolean"},
		"new_transaction_allowed": map[string]any{"type": "boolean"},
		"tx_effect":               map[string]any{"type": "string"},
		"db_outcome":              map[string]any{"type": "string"},
		"audit_durability":        map[string]any{"type": "string"},
		"connection_disposition":  map[string]any{"type": "string"},
		"terminal_event_digest":   map[string]any{"type": "string"},
		"data":                    data,
	}
	return map[string]any{"type": "object", "properties": properties,
		"required": []string{"decision", "reason", "suggestion"}, "additionalProperties": false}
}

func addAgentTool[In any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, ToolResponse]) {
	input, err := jsonschema.ForType(reflect.TypeFor[In](), &jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	// The SDK describes optional pointers and nil-able slices as nullable
	// unions. On the wire expected_seq may be omitted and a null statements
	// plan is invalid, so single types express the actual accepted contract.
	if property := input.Properties["expected_seq"]; property != nil {
		property.Types = nil
		property.Type = "integer"
	}
	if property := input.Properties["statements"]; property != nil {
		property.Types = nil
		property.Type = "array"
	}
	tool.InputSchema = input
	tool.OutputSchema = portableToolResponseSchema(tool.Name)
	mcp.AddTool(server, tool, handler)
}

func registerTools(server *mcp.Server, handlers *toolHandlers) {
	if handlers.agent.TaskGrantID != "" {
		addAgentTool(server, &mcp.Tool{
			Name:        "query",
			Description: "执行任务授权限定的单条 SELECT；按当前权限裁剪结果并记录审计。",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input sqlInput) (*mcp.CallToolResult, ToolResponse, error) {
			response := handlers.query(ctx, input.DatasourceID, input.SQL)
			return protocolResult(response), response, nil
		})
		return
	}
	addAgentTool(server, &mcp.Tool{
		Name:        "list_datasources",
		Description: "列出当前 Agent 获授权的数据源；仅返回公开标识信息，不返回连接参数或密钥。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.listDatasources(ctx)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "list_schema",
		Description: "读取授权数据源结构并按列权限裁剪；可指定安全的 table 或 schema.table。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listSchemaInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.listSchema(ctx, input.DatasourceID, input.Table)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "explain_query",
		Description: "只评估一条 SQL，不执行查询；已验证计划能力的方言会附带执行计划，可能返回拒绝、告警或需审批结论。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sqlInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.explainQuery(ctx, input.DatasourceID, input.SQL)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "query",
		Description: "执行受保护的单条 SELECT；结果自动限行和脱敏，危险查询可能被拦截或转审批。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sqlInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.query(ctx, input.DatasourceID, input.SQL)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "find_documents",
		Description: "在授权 MongoDB 集合执行受控只读查询；仅返回指定字段，按规则脱敏并写入持久审计。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input mongoFindInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.findDocuments(ctx, input)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "read_key",
		Description: "读取授权 Redis/Valkey 单个 UTF-8 字符串键；最多 64 KiB，按键权限、脱敏规则和持久审计处理。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input keyReadInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.readKey(ctx, input)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "query_analytics",
		Description: "在精确授权的 ClickHouse MergeTree 单表读取显式列；限制等值过滤、资源和返回行数，执行脱敏及持久审计。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input analyticsInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.queryAnalytics(ctx, input)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "execute_write",
		Description: "执行受保护的 INSERT、UPDATE、DELETE 或 DDL；必须说明 reason，可能被拦截或转审批。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input writeInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.executeWrite(ctx, input.DatasourceID, input.SQL, input.Reason)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "request_approval",
		Description: "将 SQL 单向提升为人工审批，不执行 SQL；必须提供 reason。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input approvalInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.requestApproval(ctx, input.DatasourceID, input.SQL, input.Reason)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "get_approval_result",
		Description: "查询当前 Agent 自己的审批单状态；其他 Agent 的单号按未找到处理。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input approvalResultInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.getApprovalResult(ctx, input.ApprovalID)
		return protocolResult(response), response, nil
	})
	addAgentTool(server, &mcp.Tool{
		Name:        "execute_approved",
		Description: "对已批准且未过期的审批单执行一次封存 SQL；仅接受 approval_id，执行时重新检查身份、策略和规则。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input approvalResultInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.executeApproved(ctx, input.ApprovalID)
		return protocolResult(response), response, nil
	})
}

func protocolResult(response ToolResponse) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: response.Decision == "error"}
}
