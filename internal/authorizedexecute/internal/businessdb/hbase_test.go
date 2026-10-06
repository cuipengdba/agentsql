package businessdb

import (
	"testing"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/tsuna/gohbase/pb"
)

func TestHBaseConnectionAndPolicy(t *testing.T) {
	opts, err := hbaseConnection(model.Datasource{DBType: "hbase", Host: "localhost", Port: 2181, Username: "alice"}, "")
	if err != nil || opts.quorum != "localhost:2181" || opts.user != "alice" {
		t.Fatalf("options: %+v %v", opts, err)
	}
	for _, ds := range []model.Datasource{{DBType: "hbase", Host: "localhost"}, {DBType: "hbase", Host: "localhost", Port: 16020}, {DBType: "hbase", Host: "localhost", Port: 2181, TLSMode: "strict"}} {
		if _, err := hbaseConnection(ds, ""); err == nil {
			t.Errorf("unsupported connection accepted: %+v", ds)
		}
	}
	if _, err := hbaseConnection(model.Datasource{DBType: "hbase", Host: "localhost", Port: 2181}, "secret"); err == nil {
		t.Fatal("password accepted")
	}
	for _, command := range []string{"GET", "SCAN", "LIST", "COUNT"} {
		if err := hbaseCommandAllowed(command, true); err != nil {
			t.Error(err)
		}
	}
	for _, command := range []string{"PUT", "DELETE", "INCR", "APPEND"} {
		if hbaseCommandAllowed(command, true) == nil || hbaseCommandAllowed(command, false) != nil {
			t.Errorf("write policy: %s", command)
		}
	}
	for _, command := range []string{"CREATE", "DROP", "DISABLE", "SNAPSHOT", "FLUSH", "GRANT"} {
		if hbaseCommandAllowed(command, false) == nil {
			t.Errorf("dangerous command accepted: %s", command)
		}
	}
}
func TestHBaseScanAndDiscovery(t *testing.T) {
	for _, args := range [][]string{{"table"}, {"table", "0"}, {"table", "1001"}} {
		if _, err := hbaseScanLimit(args); err == nil {
			t.Errorf("unbounded scan accepted: %v", args)
		}
	}
	if n, err := hbaseScanLimit([]string{"table", "10"}); err != nil || n != 10 {
		t.Fatalf("bounded scan: %d %v", n, err)
	}
	ns := hbaseNamespaces([]*pb.TableName{{Namespace: []byte("default"), Qualifier: []byte("users")}})
	if len(ns) != 1 || ns[0].Name != "users" || ns[0].Metadata["namespace"] != "default" {
		t.Fatalf("namespaces: %+v", ns)
	}
}
