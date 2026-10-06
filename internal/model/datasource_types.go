package model

// DatasourceCategory identifies a connection family's capabilities. Native
// sources in v0.5 have connection-level support, not SQL pipeline support.
type DatasourceCategory string

const (
	CategoryRelational DatasourceCategory = "relational"
	CategoryKeyValue   DatasourceCategory = "keyvalue"
	CategoryDocument   DatasourceCategory = "document"
	CategoryWideColumn DatasourceCategory = "widecolumn"
	CategoryGraph      DatasourceCategory = "graph"
	CategoryTimeSeries DatasourceCategory = "timeseries"
	CategoryOLAP       DatasourceCategory = "olap"
	CategoryVector     DatasourceCategory = "vector"
)

type DatasourceTypeSpec struct {
	Category         DatasourceCategory
	DisplayName      string
	DefaultPort      int
	RequiresDatabase bool
	RequiresUsername bool
	RequiresPassword bool
}

var datasourceTypes = map[string]DatasourceTypeSpec{
	"postgres":  {CategoryRelational, "PostgreSQL", 5432, true, true, true},
	"mysql":     {CategoryRelational, "MySQL", 3306, true, true, true},
	"dm":        {CategoryRelational, "DM", 5236, true, true, true},
	"oracle":    {CategoryRelational, "Oracle", 1521, true, true, true},
	"yashan":    {CategoryRelational, "YashanDB", 1688, true, true, true},
	"sqlserver": {CategoryRelational, "SQL Server", 1433, true, true, true},
	"redis":     {CategoryKeyValue, "Redis", 6379, false, false, false},
	"valkey":    {CategoryKeyValue, "Valkey", 6379, false, false, false},
	"memcached": {CategoryKeyValue, "Memcached", 11211, false, false, false},
	// Connection logic for the following 18 native types awaits later batches.
	"mongodb":     {CategoryDocument, "MongoDB", 27017, true, true, true},
	"couchbase":   {CategoryDocument, "Couchbase", 11210, true, true, true},
	"couchdb":     {CategoryDocument, "CouchDB", 5984, true, true, true},
	"cassandra":   {CategoryWideColumn, "Cassandra", 9042, true, true, true},
	"hbase":       {CategoryWideColumn, "HBase", 16020, true, true, true},
	"scylla":      {CategoryWideColumn, "ScyllaDB", 9042, true, true, true},
	"neo4j":       {CategoryGraph, "Neo4j", 7687, true, true, true},
	"janusgraph":  {CategoryGraph, "JanusGraph", 8182, true, true, true},
	"nebula":      {CategoryGraph, "NebulaGraph", 9669, true, true, true},
	"influxdb":    {CategoryTimeSeries, "InfluxDB", 8086, true, true, true},
	"prometheus":  {CategoryTimeSeries, "Prometheus", 9090, true, true, true},
	"timescaledb": {CategoryTimeSeries, "TimescaleDB", 5432, true, true, true},
	"clickhouse":  {CategoryOLAP, "ClickHouse", 9000, true, true, true},
	"doris":       {CategoryOLAP, "Doris", 9030, true, true, true},
	"starrocks":   {CategoryOLAP, "StarRocks", 9030, true, true, true},
	"milvus":      {CategoryVector, "Milvus", 19530, true, true, true},
	"qdrant":      {CategoryVector, "Qdrant", 6334, true, true, true},
	"weaviate":    {CategoryVector, "Weaviate", 8080, true, true, true},
}

func TypeSpecFor(dbType string) (DatasourceTypeSpec, bool) {
	spec, ok := datasourceTypes[dbType]
	return spec, ok
}
func CategoryOf(dbType string) (DatasourceCategory, bool) {
	spec, ok := TypeSpecFor(dbType)
	return spec.Category, ok
}
func IsNative(dbType string) bool {
	category, ok := CategoryOf(dbType)
	return ok && category != CategoryRelational
}
