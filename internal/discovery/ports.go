package discovery

import "context"

// SchemaLister returns metadata only for the explicitly requested typed refs.
type SchemaLister interface {
	ListColumns(ctx context.Context, datasourceID string, tables []TableRef) ([]ColumnMeta, error)
}

// LimitedQuerier samples metadata-confirmed typed refs with an enforced row
// limit. It never accepts SQL text.
type LimitedQuerier interface {
	QueryColumns(
		ctx context.Context,
		datasourceID string,
		table TableRef,
		columns []ColumnRef,
		limit int,
	) (SampleBatch, error)
}
