package mcpserver

import (
	"context"

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

func registerTools(server *mcp.Server, handlers *toolHandlers) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_datasources",
		Description: "列出当前 Agent 获授权的数据源；仅返回公开标识信息，不返回连接参数或密钥。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.listDatasources(ctx)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_schema",
		Description: "读取授权数据源结构并按列权限裁剪；可指定安全的 table 或 schema.table。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input listSchemaInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.listSchema(ctx, input.DatasourceID, input.Table)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "explain_query",
		Description: "只评估和 EXPLAIN 一条 SQL，不执行；可能返回拒绝、告警或需审批结论。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sqlInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.explainQuery(ctx, input.DatasourceID, input.SQL)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "query",
		Description: "执行受保护的单条 SELECT；结果自动限行和脱敏，危险查询可能被拦截或转审批。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sqlInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.query(ctx, input.DatasourceID, input.SQL)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "execute_write",
		Description: "执行受保护的 INSERT、UPDATE、DELETE 或 DDL；必须说明 reason，可能被拦截或转审批。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input writeInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.executeWrite(ctx, input.DatasourceID, input.SQL, input.Reason)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "request_approval",
		Description: "将 SQL 单向提升为人工审批，不执行 SQL；必须提供 reason。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input approvalInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.requestApproval(ctx, input.DatasourceID, input.SQL, input.Reason)
		return protocolResult(response), response, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_approval_result",
		Description: "查询当前 Agent 自己的审批单状态；其他 Agent 的单号按未找到处理。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input approvalResultInput) (*mcp.CallToolResult, ToolResponse, error) {
		response := handlers.getApprovalResult(ctx, input.ApprovalID)
		return protocolResult(response), response, nil
	})
}

func protocolResult(response ToolResponse) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: response.Decision == "error"}
}
