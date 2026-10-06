package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoExecutor struct {
	client                 *mongo.Client
	database, datasourceID string
	readOnly               bool
}

func mongoURI(ds model.Datasource, password string) (string, error) {
	if strings.TrimSpace(ds.Host) == "" {
		return "", fmt.Errorf("mongodb host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 27017
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("mongodb port is invalid")
	}
	if ds.Username == "" && password != "" {
		return "", fmt.Errorf("mongodb password requires username")
	}
	u := &url.URL{Scheme: "mongodb", Host: net.JoinHostPort(ds.Host, strconv.Itoa(port)), Path: "/"}
	if ds.Username != "" {
		u.User = url.UserPassword(ds.Username, password)
	}
	q := url.Values{}
	if ds.Username != "" {
		q.Set("authSource", "admin")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func NewMongoExecutor(ds model.Datasource, password string, readOnly bool) (*MongoExecutor, error) {
	if ds.DBType != "mongodb" {
		return nil, fmt.Errorf("invalid mongodb dialect")
	}
	uri, err := mongoURI(ds, password)
	if err != nil {
		return nil, err
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	limit, err := normalizedConnectionLimit(ds.ConnLimit)
	if err != nil {
		return nil, err
	}
	if ds.TrustServerCertificate {
		return nil, fmt.Errorf("unverified mongodb TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
		return nil, fmt.Errorf("invalid mongodb TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return nil, fmt.Errorf("mongodb TLS settings require tls_mode")
	}
	opts := options.Client().ApplyURI(uri).SetServerSelectionTimeout(time.Duration(timeout) * time.Millisecond).SetConnectTimeout(time.Duration(timeout) * time.Millisecond).SetSocketTimeout(time.Duration(timeout) * time.Millisecond).SetTimeout(time.Duration(timeout) * time.Millisecond).SetMaxPoolSize(uint64(limit))
	if ds.TLSMode != "" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ds.TLSServerName}
		if ds.TLSCAFile != "" {
			pem, err := os.ReadFile(ds.TLSCAFile)
			if err != nil {
				return nil, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid mongodb TLS CA")
			}
			cfg.RootCAs = roots
		}
		opts.SetTLSConfig(cfg)
	}
	client, err := mongo.Connect(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	database := ds.Database
	if database == "" {
		database = "admin"
	}
	executor := &MongoExecutor{client: client, database: database, datasourceID: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()
	if err := executor.Ping(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return executor, nil
}
func (e *MongoExecutor) Category() model.DatasourceCategory { return model.CategoryDocument }
func (e *MongoExecutor) Dialect() model.DBDialect           { return "mongodb" }
func (e *MongoExecutor) Ping(ctx context.Context) error     { return e.client.Ping(ctx, nil) }
func (e *MongoExecutor) Close() error                       { return e.client.Disconnect(context.Background()) }
func (e *MongoExecutor) ServerVersion(ctx context.Context) (string, error) {
	var status bson.M
	if err := e.client.Database("admin").RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&status); err != nil {
		return "", err
	}
	return parseMongoVersion(status)
}
func parseMongoVersion(status bson.M) (string, error) {
	v, ok := status["version"].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("mongodb version unavailable")
	}
	return boundedNativeString(v), nil
}
func appendMongoCollections(cat *NativeCatalog, database string, collections []string) {
	for _, collection := range collections {
		if len(cat.Namespaces) >= nativeMaxRows {
			break
		}
		cat.Namespaces = append(cat.Namespaces, NativeNamespace{Name: database + "." + collection, Kind: "collection", Metadata: map[string]string{"database": database, "collection": collection}})
	}
}
func (e *MongoExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	names, err := e.client.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return NativeCatalog{}, err
	}
	cat := NativeCatalog{Category: model.CategoryDocument, ServerVersion: v}
	for _, name := range names {
		if len(cat.Namespaces) >= nativeMaxRows {
			break
		}
		cat.Namespaces = append(cat.Namespaces, NativeNamespace{Name: name, Kind: "database"})
		collections, err := e.client.Database(name).ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return NativeCatalog{}, err
		}
		appendMongoCollections(&cat, name, collections)
	}
	return cat, nil
}

var mongoReads = map[string]bool{"FIND": true, "COUNTDOCUMENTS": true, "DISTINCT": true, "AGGREGATE": true, "LISTCOLLECTIONS": true, "SERVERSTATUS": true, "DBSTATS": true, "COLLECTIONSTATS": true, "PING": true, "HELLO": true, "EXPLAIN": true}
var mongoWrites = map[string]bool{"INSERTONE": true, "INSERTMANY": true, "UPDATEONE": true, "UPDATEMANY": true, "DELETEONE": true, "DELETEMANY": true, "FINDANDMODIFY": true}

func mongoCommandAllowed(command string, readOnly bool) error {
	if mongoReads[command] {
		return nil
	}
	if mongoWrites[command] {
		if readOnly {
			return fmt.Errorf("native write command %s is forbidden in read-only mode", command)
		}
		return nil
	}
	return fmt.Errorf("native command %s is not allowed", command)
}
func mongoParams(args []string) (bson.M, error) {
	if len(args) == 0 {
		return bson.M{}, nil
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("mongodb command requires one JSON argument")
	}
	var p bson.M
	if err := bson.UnmarshalExtJSON([]byte(args[0]), false, &p); err != nil || p == nil {
		return nil, fmt.Errorf("invalid mongodb JSON arguments")
	}
	return p, nil
}
func mongoObject(p bson.M, key string, required bool) (bson.M, error) {
	v, ok := p[key]
	if !ok {
		if required {
			return nil, fmt.Errorf("mongodb %s is required", key)
		}
		return bson.M{}, nil
	}
	switch x := v.(type) {
	case bson.M:
		return x, nil
	case map[string]any:
		return bson.M(x), nil
	default:
		return nil, fmt.Errorf("mongodb %s must be an object", key)
	}
}
func mongoCollection(p bson.M) (string, error) {
	s, ok := p["collection"].(string)
	if !ok || s == "" || strings.HasPrefix(s, "system.") || strings.ContainsAny(s, "\x00$") {
		return "", fmt.Errorf("mongodb collection is invalid")
	}
	return s, nil
}
func mongoSafeFields(v any) bool {
	switch x := v.(type) {
	case bson.M:
		for k, val := range x {
			if strings.ContainsAny(k, "$\x00") || !mongoSafeFields(val) {
				return false
			}
		}
		return true
	case map[string]any:
		return mongoSafeFields(bson.M(x))
	case bson.D:
		for _, item := range x {
			if strings.ContainsAny(item.Key, "$\x00") || !mongoSafeFields(item.Value) {
				return false
			}
		}
		return true
	case bson.A:
		for _, val := range x {
			if !mongoSafeFields(val) {
				return false
			}
		}
		return true
	case []any:
		for _, val := range x {
			if !mongoSafeFields(val) {
				return false
			}
		}
		return true
	case nil, bool, string, int32, int64, float64, primitive.ObjectID:
		return true
	}
	return false
}
func mongoSafeFilter(filter bson.M) bool { return mongoSafeFields(filter) }
func mongoLimit(p bson.M) (int64, error) {
	v, ok := p["limit"]
	if !ok {
		return nativeMaxRows, nil
	}
	var n int64
	switch x := v.(type) {
	case int32:
		n = int64(x)
	case int64:
		n = x
	case float64:
		if x != float64(int64(x)) {
			return 0, fmt.Errorf("invalid mongodb limit")
		}
		n = int64(x)
	default:
		return 0, fmt.Errorf("invalid mongodb limit")
	}
	if n < 1 || n > nativeMaxRows {
		return 0, fmt.Errorf("mongodb limit must be 1 to %d", nativeMaxRows)
	}
	return n, nil
}
func mongoJSON(v any) string {
	b, err := bson.MarshalExtJSON(v, false, false)
	if err != nil {
		return ""
	}
	return boundedNativeString(string(b))
}
func mongoCursorRows(ctx context.Context, cur *mongo.Cursor) (NativeQueryResult, error) {
	defer cur.Close(ctx)
	rows := make([][]string, 0)
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return NativeQueryResult{}, err
		}
		if len(rows) >= nativeMaxRows {
			break
		}
		rows = append(rows, []string{mongoJSON(doc)})
	}
	if err := cur.Err(); err != nil {
		return NativeQueryResult{}, err
	}
	return boundedNativeRows([]string{"document"}, rows), nil
}
func (e *MongoExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.Dialect(), e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if err = mongoCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	p, err := mongoParams(args)
	if err != nil {
		return result, err
	}
	dbname := req.Namespace
	if dbname == "" {
		dbname = e.database
	}
	if strings.ContainsAny(dbname, "\x00/ ") {
		return result, fmt.Errorf("invalid mongodb namespace")
	}
	db := e.client.Database(dbname)
	if command == "PING" {
		err = e.Ping(ctx)
		return NativeQueryResult{Raw: "OK"}, err
	}
	if command == "LISTCOLLECTIONS" {
		names, x := db.ListCollectionNames(ctx, bson.D{})
		err = x
		if err != nil {
			return result, err
		}
		rows := [][]string{}
		for _, n := range names {
			rows = append(rows, []string{n})
		}
		return boundedNativeRows([]string{"collection"}, rows), nil
	}
	if command == "SERVERSTATUS" || command == "DBSTATS" || command == "HELLO" {
		cmd := map[string]string{"SERVERSTATUS": "serverStatus", "DBSTATS": "dbStats", "HELLO": "hello"}[command]
		var doc bson.M
		err = db.RunCommand(ctx, bson.D{{Key: cmd, Value: 1}}).Decode(&doc)
		return NativeQueryResult{Raw: mongoJSON(doc)}, err
	}
	collection, err := mongoCollection(p)
	if err != nil {
		return result, err
	}
	col := db.Collection(collection)
	if command == "COLLECTIONSTATS" {
		var doc bson.M
		err = db.RunCommand(ctx, bson.D{{Key: "collStats", Value: collection}}).Decode(&doc)
		return NativeQueryResult{Raw: mongoJSON(doc)}, err
	}
	filter, err := mongoObject(p, "filter", false)
	if err != nil {
		return result, err
	}
	if !mongoSafeFilter(filter) {
		return result, fmt.Errorf("mongodb filter operator is not allowed")
	}
	switch command {
	case "FIND", "EXPLAIN":
		limit, x := mongoLimit(p)
		if x != nil {
			return result, x
		}
		if command == "EXPLAIN" {
			var doc bson.M
			err = db.RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: collection}, {Key: "filter", Value: filter}, {Key: "limit", Value: limit}}}}).Decode(&doc)
			return NativeQueryResult{Raw: mongoJSON(doc)}, err
		}
		cur, x := col.Find(ctx, filter, options.Find().SetLimit(limit))
		if x != nil {
			return result, x
		}
		return mongoCursorRows(ctx, cur)
	case "COUNTDOCUMENTS":
		n, x := col.CountDocuments(ctx, filter)
		return boundedNativeRows([]string{"count"}, [][]string{{strconv.FormatInt(n, 10)}}), x
	case "DISTINCT":
		field, ok := p["field"].(string)
		if !ok || field == "" || strings.HasPrefix(field, "$") {
			return result, fmt.Errorf("mongodb distinct field required")
		}
		values, x := col.Distinct(ctx, field, filter)
		if x != nil {
			return result, x
		}
		rows := [][]string{}
		for _, v := range values {
			rows = append(rows, []string{mongoJSON(bson.M{"value": v})})
		}
		return boundedNativeRows([]string{"value"}, rows), nil
	case "AGGREGATE":
		pipeline, ok := p["pipeline"].(bson.A)
		if !ok || len(pipeline) == 0 || len(pipeline) > 32 {
			return result, fmt.Errorf("invalid mongodb pipeline")
		}
		stages := mongo.Pipeline{}
		for _, raw := range pipeline {
			stage, ok := raw.(bson.M)
			if !ok || len(stage) != 1 {
				return result, fmt.Errorf("invalid mongodb stage")
			}
			for key, value := range stage {
				if key != "$match" && key != "$limit" && key != "$skip" {
					return result, fmt.Errorf("mongodb aggregate stage %s is not allowed", key)
				}
				if key == "$match" && !mongoSafeFields(value) {
					return result, fmt.Errorf("unsafe mongodb match")
				}
				if key == "$limit" || key == "$skip" {
					n, ok := value.(int32)
					if !ok || n < 0 || n > nativeMaxRows || key == "$limit" && n == 0 {
						return result, fmt.Errorf("invalid mongodb stage limit")
					}
				}
				stages = append(stages, bson.D{{Key: key, Value: value}})
			}
		}
		stages = append(stages, bson.D{{Key: "$limit", Value: nativeMaxRows}})
		cur, x := col.Aggregate(ctx, stages)
		if x != nil {
			return result, x
		}
		return mongoCursorRows(ctx, cur)
	case "INSERTONE":
		doc, x := mongoObject(p, "document", true)
		if x != nil {
			return result, x
		}
		if !mongoSafeFields(doc) {
			return result, fmt.Errorf("unsafe mongodb document")
		}
		r, x := col.InsertOne(ctx, doc)
		if x != nil {
			return NativeQueryResult{}, x
		}
		return NativeQueryResult{Raw: mongoJSON(bson.M{"insertedId": r.InsertedID})}, x
	case "INSERTMANY":
		docs, ok := p["documents"].(bson.A)
		if !ok || len(docs) == 0 || len(docs) > nativeMaxRows || !mongoSafeFields(docs) {
			return result, fmt.Errorf("invalid mongodb documents")
		}
		values := make([]any, len(docs))
		for i, v := range docs {
			values[i] = v
		}
		r, x := col.InsertMany(ctx, values)
		if x != nil {
			return result, x
		}
		return NativeQueryResult{Raw: mongoJSON(bson.M{"insertedIds": r.InsertedIDs})}, nil
	case "UPDATEONE", "UPDATEMANY", "FINDANDMODIFY":
		if len(filter) == 0 {
			return result, fmt.Errorf("mongodb update requires filter")
		}
		update, x := mongoObject(p, "update", true)
		if x != nil {
			return result, x
		}
		if len(update) == 0 {
			return result, fmt.Errorf("empty mongodb update")
		}
		for op, v := range update {
			if op != "$set" && op != "$unset" && op != "$inc" || !mongoSafeFields(v) {
				return result, fmt.Errorf("mongodb update operator is not allowed")
			}
		}
		if command == "FINDANDMODIFY" {
			var doc bson.M
			x = col.FindOneAndUpdate(ctx, filter, update).Decode(&doc)
			return NativeQueryResult{Raw: mongoJSON(doc)}, x
		}
		var n int64
		if command == "UPDATEONE" {
			r, y := col.UpdateOne(ctx, filter, update)
			x = y
			if x == nil {
				n = r.ModifiedCount
			}
		} else {
			r, y := col.UpdateMany(ctx, filter, update)
			x = y
			if x == nil {
				n = r.ModifiedCount
			}
		}
		return NativeQueryResult{Raw: strconv.FormatInt(n, 10)}, x
	case "DELETEONE", "DELETEMANY":
		if len(filter) == 0 {
			return result, fmt.Errorf("mongodb delete requires filter")
		}
		var n int64
		if command == "DELETEONE" {
			r, x := col.DeleteOne(ctx, filter)
			err = x
			if x == nil {
				n = r.DeletedCount
			}
		} else {
			r, x := col.DeleteMany(ctx, filter)
			err = x
			if x == nil {
				n = r.DeletedCount
			}
		}
		return NativeQueryResult{Raw: strconv.FormatInt(n, 10)}, err
	}
	return result, fmt.Errorf("mongodb command is not implemented")
}
