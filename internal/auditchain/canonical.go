package auditchain

import "strconv"

// Row is the complete V1 canonical business row. Pointer fields distinguish
// SQL NULL from a present zero value (including an empty string or integer 0).
type Row struct {
	ID             int64   `json:"id"`
	TS             string  `json:"ts"`
	AgentID        *string `json:"agent_id"`
	DatasourceID   *string `json:"datasource_id"`
	SessionID      *string `json:"session_id"`
	ConversationID *string `json:"conversation_id"`
	MCPTool        *string `json:"mcp_tool"`
	DBType         *string `json:"db_type"`
	SQLRaw         *string `json:"sql_raw"`
	SQLNorm        *string `json:"sql_norm"`
	StmtType       *string `json:"stmt_type"`
	Objects        *string `json:"objects"`
	Decision       string  `json:"decision"`
	RuleHits       *string `json:"rule_hits"`
	RiskLevel      *int64  `json:"risk_level"`
	EstRows        *int64  `json:"est_rows"`
	RowsReturned   *int64  `json:"rows_returned"`
	LatencyMS      *int64  `json:"latency_ms"`
	ClientIP       *string `json:"client_ip"`
	ModelName      *string `json:"model_name"`
	ErrorMsg       *string `json:"error_msg"`
	ErrorCode      *string `json:"error_code"`
	Action         *string `json:"action"`
	ActorType      *string `json:"actor_type"`
	ActorID        *string `json:"actor_id"`
	DetailsJSON    *string `json:"details_json"`
	EventUUID      *string `json:"event_uuid"`
}

// EncodeCanonical serializes all 27 V1 fields in the frozen E3 order.
// String bytes are used verbatim; JSON and Unicode are never normalized.
func EncodeCanonical(row Row) ([]byte, error) {
	items := []Item{
		intItem(row.ID),
		textItem(row.TS),
		nullableTextItem(row.AgentID),
		nullableTextItem(row.DatasourceID),
		nullableTextItem(row.SessionID),
		nullableTextItem(row.ConversationID),
		nullableTextItem(row.MCPTool),
		nullableTextItem(row.DBType),
		nullableTextItem(row.SQLRaw),
		nullableTextItem(row.SQLNorm),
		nullableTextItem(row.StmtType),
		nullableTextItem(row.Objects),
		textItem(row.Decision),
		nullableTextItem(row.RuleHits),
		nullableIntItem(row.RiskLevel),
		nullableIntItem(row.EstRows),
		nullableIntItem(row.RowsReturned),
		nullableIntItem(row.LatencyMS),
		nullableTextItem(row.ClientIP),
		nullableTextItem(row.ModelName),
		nullableTextItem(row.ErrorMsg),
		nullableTextItem(row.ErrorCode),
		nullableTextItem(row.Action),
		nullableTextItem(row.ActorType),
		nullableTextItem(row.ActorID),
		nullableTextItem(row.DetailsJSON),
		nullableTextItem(row.EventUUID),
	}
	return EncodeTLV(items...)
}

func textItem(value string) Item {
	return Item{Tag: TagText, Payload: []byte(value)}
}

func intItem(value int64) Item {
	return Item{Tag: TagInt, Payload: []byte(strconv.FormatInt(value, 10))}
}

func bytesItem(value []byte) Item {
	return Item{Tag: TagBytes, Payload: value}
}

func nullableTextItem(value *string) Item {
	if value == nil {
		return Item{Tag: TagNil}
	}
	return textItem(*value)
}

func nullableIntItem(value *int64) Item {
	if value == nil {
		return Item{Tag: TagNil}
	}
	return intItem(*value)
}
