package businessdb

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/tsuna/gohbase"
	"github.com/tsuna/gohbase/hrpc"
	"github.com/tsuna/gohbase/pb"
)

// gohbase discovers HBase RPC endpoints through ZooKeeper. Port 16020 cannot
// serve as its bootstrap endpoint; callers must explicitly configure 2181.
type HBaseExecutor struct {
	client       gohbase.Client
	admin        gohbase.AdminClient
	datasourceID string
	readOnly     bool
}
type hbaseOptions struct {
	quorum, user string
	timeout      time.Duration
}

func hbaseConnection(ds model.Datasource, password string) (hbaseOptions, error) {
	if ds.DBType != "hbase" {
		return hbaseOptions{}, fmt.Errorf("invalid HBase dialect")
	}
	if strings.TrimSpace(ds.Host) == "" {
		return hbaseOptions{}, fmt.Errorf("hbase host is required")
	}
	if ds.Port == 0 || ds.Port == 16020 {
		return hbaseOptions{}, fmt.Errorf("gohbase requires an explicit ZooKeeper port (usually 2181); region-server port 16020 is unsupported")
	}
	if ds.Port < 1 || ds.Port > 65535 {
		return hbaseOptions{}, fmt.Errorf("hbase port is invalid")
	}
	if password != "" {
		return hbaseOptions{}, fmt.Errorf("HBase password authentication is unsupported")
	}
	if ds.TLSMode != "" || ds.TLSCAFile != "" || ds.TLSServerName != "" || ds.TrustServerCertificate {
		return hbaseOptions{}, fmt.Errorf("HBase TLS is unsupported by this adapter")
	}
	if ds.Database != "" {
		return hbaseOptions{}, fmt.Errorf("HBase database field is unsupported")
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return hbaseOptions{}, err
	}
	if _, err := normalizedConnectionLimit(ds.ConnLimit); err != nil {
		return hbaseOptions{}, err
	}
	return hbaseOptions{quorum: fmt.Sprintf("%s:%d", ds.Host, ds.Port), user: ds.Username, timeout: time.Duration(timeout) * time.Millisecond}, nil
}
func NewHBaseExecutor(ds model.Datasource, password string, readOnly bool) (*HBaseExecutor, error) {
	opts, err := hbaseConnection(ds, password)
	if err != nil {
		return nil, err
	}
	options := []gohbase.Option{gohbase.ZookeeperTimeout(opts.timeout), gohbase.RegionLookupTimeout(opts.timeout), gohbase.RegionReadTimeout(opts.timeout)}
	if opts.user != "" {
		options = append(options, gohbase.EffectiveUser(opts.user))
	}
	client := gohbase.NewClient(opts.quorum, options...)
	admin := gohbase.NewAdminClient(opts.quorum, options...)
	e := &HBaseExecutor{client: client, admin: admin, datasourceID: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (e *HBaseExecutor) Category() model.DatasourceCategory { return model.CategoryWideColumn }
func (e *HBaseExecutor) Dialect() model.DBDialect           { return "hbase" }
func (e *HBaseExecutor) Close() error {
	e.client.Close()
	if closer, ok := e.admin.(interface{ Close() }); ok {
		closer.Close()
	}
	return nil
}
func (e *HBaseExecutor) tableNames(ctx context.Context) ([]*pb.TableName, error) {
	req, err := hrpc.NewListTableNames(ctx)
	if err != nil {
		return nil, err
	}
	return e.admin.ListTableNames(req)
}
func (e *HBaseExecutor) Ping(ctx context.Context) error { _, err := e.tableNames(ctx); return err }
func (e *HBaseExecutor) ServerVersion(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	status, err := e.admin.ClusterStatus()
	if err != nil {
		return "", err
	}
	if status.GetHbaseVersion() == nil || status.GetHbaseVersion().GetVersion() == "" {
		return "", fmt.Errorf("HBase server version is unavailable")
	}
	return boundedNativeString(status.GetHbaseVersion().GetVersion()), nil
}
func hbaseNamespaces(names []*pb.TableName) []NativeNamespace {
	out := make([]NativeNamespace, 0, min(len(names), nativeMaxRows))
	for _, name := range names {
		if len(out) >= nativeMaxRows {
			break
		}
		out = append(out, NativeNamespace{Name: string(name.GetQualifier()), Kind: "table", Metadata: map[string]string{"namespace": string(name.GetNamespace())}})
	}
	return out
}
func (e *HBaseExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	names, err := e.tableNames(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	return NativeCatalog{Category: model.CategoryWideColumn, ServerVersion: version, Namespaces: hbaseNamespaces(names)}, nil
}
func hbaseCommandAllowed(command string, readOnly bool) error {
	switch command {
	case "GET", "SCAN", "LIST", "COUNT":
		return nil
	case "PUT", "DELETE", "INCR", "APPEND":
		if readOnly {
			return fmt.Errorf("native write command %s is forbidden in read-only mode", command)
		}
		return nil
	}
	return fmt.Errorf("native command %s is not allowed", command)
}
func hbaseScanLimit(args []string) (int, error) {
	if len(args) != 2 && len(args) != 4 {
		return 0, fmt.Errorf("SCAN requires table, LIMIT, and optional STARTROW/STOPROW")
	}
	limit, err := strconv.Atoi(args[1])
	if err != nil || limit < 1 || limit > nativeMaxRows {
		return 0, fmt.Errorf("SCAN LIMIT must be 1 to %d", nativeMaxRows)
	}
	return limit, nil
}
func hbaseResultRows(result *hrpc.Result) [][]string {
	if result == nil {
		return nil
	}
	rows := make([][]string, 0, min(len(result.Cells), nativeMaxRows))
	for _, c := range result.Cells {
		if len(rows) >= nativeMaxRows {
			break
		}
		rows = append(rows, []string{string(c.Row), string(c.Family), string(c.Qualifier), string(c.Value)})
	}
	return rows
}
func (e *HBaseExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("hbase", e.datasourceID, command, e.readOnly, err) }()
	var args []string
	command, args, err = normalizedNativeCommand(request)
	if err != nil {
		return result, err
	}
	if err = hbaseCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	if command == "LIST" {
		if len(args) != 0 {
			return result, fmt.Errorf("LIST takes no arguments")
		}
		var names []*pb.TableName
		names, err = e.tableNames(ctx)
		if err != nil {
			return result, err
		}
		rows := make([][]string, 0, min(len(names), nativeMaxRows))
		for _, name := range names {
			if len(rows) >= nativeMaxRows {
				break
			}
			rows = append(rows, []string{string(name.GetNamespace()), string(name.GetQualifier())})
		}
		return boundedNativeRows([]string{"namespace", "table"}, rows), nil
	}
	if len(args) == 0 || args[0] == "" {
		return result, fmt.Errorf("HBase table is required")
	}
	if request.Namespace != "" && request.Namespace != args[0] {
		return result, fmt.Errorf("native namespace differs from table")
	}
	switch command {
	case "GET":
		if len(args) != 2 || args[1] == "" {
			return result, fmt.Errorf("GET requires table and row key")
		}
		var req *hrpc.Get
		req, err = hrpc.NewGetStr(ctx, args[0], args[1])
		if err != nil {
			return result, err
		}
		var value *hrpc.Result
		value, err = e.client.Get(req)
		return boundedNativeRows([]string{"row", "family", "qualifier", "value"}, hbaseResultRows(value)), err
	case "SCAN", "COUNT":
		if command == "COUNT" {
			return result, fmt.Errorf("COUNT is unsupported: unbounded table scan")
		}
		limit, x := hbaseScanLimit(args)
		if x != nil {
			return result, x
		}
		if len(args) >= 3 && args[2] == "" || len(args) == 4 && (args[3] == "" || args[2] >= args[3]) {
			return result, fmt.Errorf("invalid SCAN row bounds")
		}
		var req *hrpc.Scan
		if len(args) == 4 {
			req, err = hrpc.NewScanRangeStr(ctx, args[0], args[2], args[3], hrpc.NumberOfRows(uint32(limit)))
		} else {
			req, err = hrpc.NewScanStr(ctx, args[0], hrpc.NumberOfRows(uint32(limit)))
		}
		if err != nil {
			return result, err
		}
		scanner := e.client.Scan(req)
		defer scanner.Close()
		rows := make([][]string, 0)
		for len(rows) < limit {
			var value *hrpc.Result
			value, err = scanner.Next()
			if err == io.EOF {
				err = nil
				break
			}
			if err != nil {
				return result, err
			}
			rows = append(rows, hbaseResultRows(value)...)
			if len(rows) >= nativeMaxRows {
				break
			}
		}
		return boundedNativeRows([]string{"row", "family", "qualifier", "value"}, rows), nil
	case "PUT":
		if len(args) != 5 || args[1] == "" || args[2] == "" || args[3] == "" {
			return result, fmt.Errorf("PUT requires table, row, family, qualifier, value")
		}
		values := map[string]map[string][]byte{args[2]: {args[3]: []byte(args[4])}}
		var req *hrpc.Mutate
		req, err = hrpc.NewPutStr(ctx, args[0], args[1], values)
		if err != nil {
			return result, err
		}
		_, err = e.client.Put(req)
	case "DELETE":
		if len(args) != 2 || args[1] == "" {
			return result, fmt.Errorf("DELETE requires table and row key")
		}
		var req *hrpc.Mutate
		req, err = hrpc.NewDelStr(ctx, args[0], args[1], nil)
		if err != nil {
			return result, err
		}
		_, err = e.client.Delete(req)
	case "INCR":
		if len(args) != 5 || args[1] == "" || args[2] == "" || args[3] == "" {
			return result, fmt.Errorf("INCR requires table, row, family, qualifier, amount")
		}
		amount, parseErr := strconv.ParseInt(args[4], 10, 64)
		if parseErr != nil {
			return result, fmt.Errorf("invalid INCR amount")
		}
		req, x := hrpc.NewIncStrSingle(ctx, args[0], args[1], args[2], args[3], amount)
		if x != nil {
			return result, x
		}
		value, x := e.client.Increment(req)
		if x != nil {
			return result, x
		}
		return NativeQueryResult{Raw: strconv.FormatInt(value, 10)}, nil
	case "APPEND":
		if len(args) != 5 || args[1] == "" || args[2] == "" || args[3] == "" {
			return result, fmt.Errorf("APPEND requires table, row, family, qualifier, value")
		}
		values := map[string]map[string][]byte{args[2]: {args[3]: []byte(args[4])}}
		req, x := hrpc.NewAppStr(ctx, args[0], args[1], values)
		if x != nil {
			return result, x
		}
		value, x := e.client.Append(req)
		if x != nil {
			return result, x
		}
		return boundedNativeRows([]string{"row", "family", "qualifier", "value"}, hbaseResultRows(value)), nil
	default:
		return result, fmt.Errorf("HBase command %s is unavailable", command)
	}
	if err != nil {
		return result, err
	}
	return NativeQueryResult{Raw: "OK"}, nil
}
