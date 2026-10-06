package businessdb

import (
	"strings"
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/bson"
)

func TestMongoPolicyAndURI(t *testing.T) {
	u, err := mongoURI(model.Datasource{Host: "localhost", Username: "a@b"}, "p:1")
	if err != nil || !strings.Contains(u, "a%40b:p%3A1@") || !strings.Contains(u, "authSource=admin") {
		t.Fatalf("URI %s %v", u, err)
	}
	for _, c := range []string{"FIND", "COUNTDOCUMENTS", "DISTINCT", "AGGREGATE", "LISTCOLLECTIONS", "SERVERSTATUS", "DBSTATS", "COLLECTIONSTATS", "PING", "HELLO", "EXPLAIN"} {
		if err := mongoCommandAllowed(c, true); err != nil {
			t.Fatal(c, err)
		}
	}
	for _, c := range []string{"INSERTONE", "INSERTMANY", "UPDATEONE", "UPDATEMANY", "DELETEONE", "DELETEMANY", "FINDANDMODIFY"} {
		if mongoCommandAllowed(c, true) == nil || mongoCommandAllowed(c, false) != nil {
			t.Fatal(c)
		}
	}
	for _, c := range []string{"DROP", "DROPDATABASE", "RENAMECOLLECTION", "CREATEUSER", "DROPUSER", "SHUTDOWN", "ENABLESHARDING", "EVAL"} {
		if mongoCommandAllowed(c, false) == nil {
			t.Fatal(c)
		}
	}
	if mongoSafeFilter(bsonMap(t, `{"$where":"true"}`)) {
		t.Fatal("unsafe filter")
	}
	if _, err := mongoLimit(bsonMap(t, `{"limit":1001}`)); err == nil {
		t.Fatal("limit")
	}
	if version, err := parseMongoVersion(bson.M{"version": "7.0.0"}); err != nil || version != "7.0.0" {
		t.Fatalf("version %q %v", version, err)
	}
	cat := NativeCatalog{}
	appendMongoCollections(&cat, "db", []string{"a", "b"})
	if len(cat.Namespaces) != 2 || cat.Namespaces[1].Name != "db.b" {
		t.Fatalf("collections %+v", cat)
	}
	rows := make([][]string, nativeMaxRows+1)
	for i := range rows {
		rows[i] = []string{strings.Repeat("x", nativeMaxValueBytes+1)}
	}
	res := boundedNativeRows([]string{"value"}, rows)
	if len(res.Rows) != nativeMaxRows || len(res.Rows[0][0]) != nativeMaxValueBytes || res.Info["truncated"] != "true" {
		t.Fatalf("unbounded result: %+v", res.Info)
	}
}
func bsonMap(t *testing.T, s string) bson.M {
	t.Helper()
	p, err := mongoParams([]string{s})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMongoContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("container integration test")
	}
	ctx := dockerTestContext(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "mongo:7", ExposedPorts: []string{"27017/tcp"}, WaitingFor: wait.ForListeningPort("27017/tcp")}, Started: true})
	if err != nil {
		t.Skipf("container image unavailable: %v", err)
	}
	testcontainers.CleanupContainer(t, c)
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "27017/tcp")
	if err != nil {
		t.Fatal(err)
	}
	ds := model.Datasource{ID: "mongo-test", DBType: "mongodb", Host: host, Port: port.Int(), Database: "b52"}
	w, err := NewMongoExecutor(ds, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := NewMongoExecutor(ds, "", true)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	version, err := r.ServerVersion(ctx)
	if err != nil || version == "" {
		t.Fatalf("version %s %v", version, err)
	}
	t.Logf("mongo:7 version %s", version)
	if _, err := w.NativeQuery(ctx, NativeQueryRequest{Command: "INSERTONE", Args: []string{`{"collection":"items","document":{"name":"b52"}}`}}); err != nil {
		t.Fatal(err)
	}
	cat, err := r.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range cat.Namespaces {
		if n.Name == "b52.items" {
			found = true
		}
	}
	if !found {
		t.Fatalf("collection not found: %+v", cat)
	}
	for _, command := range []string{"FIND", "COUNTDOCUMENTS"} {
		res, err := r.NativeQuery(ctx, NativeQueryRequest{Command: command, Args: []string{`{"collection":"items","filter":{"name":"b52"}}`}})
		if err != nil || len(res.Rows) == 0 {
			t.Fatalf("%s %+v %v", command, res, err)
		}
	}
	if _, err := r.NativeQuery(ctx, NativeQueryRequest{Command: "INSERTONE", Args: []string{`{"collection":"items","document":{"x":1}}`}}); err == nil {
		t.Fatal("read-only write accepted")
	}
	if _, err := w.NativeQuery(ctx, NativeQueryRequest{Command: "DROP"}); err == nil {
		t.Fatal("drop accepted")
	}
}
