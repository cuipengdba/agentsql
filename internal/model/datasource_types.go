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
	// Document adapters provide connection-level native operations.
	"mongodb":   {CategoryDocument, "MongoDB", 27017, false, false, false},
	"couchbase": {CategoryDocument, "Couchbase", 11210, false, false, false},
	"couchdb":   {CategoryDocument, "CouchDB", 5984, false, false, false},
	// Wide-column sources have optional keyspace and credentials.
	"cassandra": {CategoryWideColumn, "Cassandra", 9042, false, false, false},
	"hbase":     {CategoryWideColumn, "HBase", 16020, false, false, false},
	"scylla":    {CategoryWideColumn, "ScyllaDB", 9042, false, false, false},
	// Graph adapters provide connection-level native operations. Database and
	// credentials are optional at registration; server authentication may require them.
	"neo4j":      {CategoryGraph, "Neo4j", 7687, false, false, false},
	"janusgraph": {CategoryGraph, "JanusGraph", 8182, false, false, false},
	"nebula":     {CategoryGraph, "NebulaGraph", 9669, false, false, false},
	// Time-series adapters expose bounded connection-level operations. InfluxDB
	// uses Database as organization and the secret as token when configured.
	"influxdb":    {CategoryTimeSeries, "InfluxDB", 8086, false, false, false},
	"prometheus":  {CategoryTimeSeries, "Prometheus", 9090, false, false, false},
	"timescaledb": {CategoryTimeSeries, "TimescaleDB", 5432, true, true, true},
	// OLAP adapters provide bounded connection-level operations. Credentials
	// and initial database are optional at registration; servers may require them.
	"clickhouse": {CategoryOLAP, "ClickHouse", 9000, false, false, false},
	"doris":      {CategoryOLAP, "Doris", 9030, false, false, false},
	"starrocks":  {CategoryOLAP, "StarRocks", 9030, false, false, false},
	// Vector credentials and database are optional at registration.
	"milvus":   {CategoryVector, "Milvus", 19530, false, false, false},
	"qdrant":   {CategoryVector, "Qdrant", 6334, false, false, false},
	"weaviate": {CategoryVector, "Weaviate", 8080, false, false, false},
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
