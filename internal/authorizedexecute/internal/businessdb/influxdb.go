package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

type InfluxDBExecutor struct {
	client                         influxdb2.Client
	httpClient                     *http.Client
	base, token, org, datasourceID string
	readOnly                       bool
}

func influxOptions(ds model.Datasource, token string) (string, *http.Client, error) {
	if ds.DBType != "influxdb" || strings.TrimSpace(ds.Host) == "" {
		return "", nil, fmt.Errorf("influxdb host is required")
	}
	if ds.Username != "" {
		return "", nil, fmt.Errorf("influxdb v1 username/password authentication is unsupported; use v2 token")
	}
	if token == "" || ds.Database == "" {
		return "", nil, fmt.Errorf("influxdb v2 token and organization (database field) are required")
	}
	port := ds.Port
	if port == 0 {
		port = 8086
	}
	if port < 1 || port > 65535 {
		return "", nil, fmt.Errorf("influxdb port is invalid")
	}
	if ds.TrustServerCertificate {
		return "", nil, fmt.Errorf("unverified influxdb TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
		return "", nil, fmt.Errorf("invalid influxdb TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return "", nil, fmt.Errorf("influxdb TLS settings require tls_mode")
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return "", nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	scheme := "http"
	if ds.TLSMode != "" {
		scheme = "https"
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ds.TLSServerName}
		if ds.TLSCAFile != "" {
			pem, err := os.ReadFile(ds.TLSCAFile)
			if err != nil {
				return "", nil, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return "", nil, fmt.Errorf("invalid influxdb TLS CA")
			}
			cfg.RootCAs = roots
		}
		transport.TLSClientConfig = cfg
	}
	return scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(port)), &http.Client{Transport: transport, Timeout: time.Duration(timeout) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func NewInfluxDBExecutor(ds model.Datasource, token string, readOnly bool) (*InfluxDBExecutor, error) {
	base, httpClient, err := influxOptions(ds, token)
	if err != nil {
		return nil, err
	}
	client := influxdb2.NewClientWithOptions(base, token, influxdb2.DefaultOptions().SetHTTPClient(httpClient))
	e := &InfluxDBExecutor{client: client, httpClient: httpClient, base: base, token: token, org: ds.Database, datasourceID: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), httpClient.Timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	// Ping is intentionally unauthenticated in the vendor client. Probe a token-
	// protected endpoint as well so a bad credential never appears connected.
	buckets, err := e.request(ctx, "/api/v2/buckets", url.Values{"org": {ds.Database}, "limit": {"1"}})
	if err != nil {
		_ = e.Close()
		return nil, err
	}
	var probe struct {
		Buckets []json.RawMessage `json:"buckets"`
	}
	if err := json.Unmarshal(buckets, &probe); err != nil || probe.Buckets == nil {
		_ = e.Close()
		return nil, fmt.Errorf("influxdb authenticated bucket probe returned invalid data")
	}
	return e, nil
}
func (*InfluxDBExecutor) Category() model.DatasourceCategory { return model.CategoryTimeSeries }
func (*InfluxDBExecutor) Dialect() model.DBDialect           { return "influxdb" }
func (e *InfluxDBExecutor) Close() error {
	e.client.Close()
	e.httpClient.CloseIdleConnections()
	return nil
}
func (e *InfluxDBExecutor) Ping(ctx context.Context) error {
	ok, err := e.client.Ping(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("influxdb ping failed")
	}
	return nil
}
func (e *InfluxDBExecutor) request(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := e.base + path
	if params != nil {
		u += "?" + params.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Token "+e.token)
	resp, err := e.httpClient.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("influxdb response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("influxdb API returned HTTP %d", resp.StatusCode)
	}
	return b, nil
}
func (e *InfluxDBExecutor) ServerVersion(ctx context.Context) (string, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/ping", nil)
	if err != nil {
		return "", err
	}
	resp, err := e.httpClient.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("influxdb ping returned HTTP %d", resp.StatusCode)
	}
	v := resp.Header.Get("X-Influxdb-Version")
	if v == "" {
		return "", fmt.Errorf("influxdb version header unavailable")
	}
	return boundedNativeString(v), nil
}

type influxQueryResponse struct {
	Results []struct {
		Series []struct {
			Name    string   `json:"name"`
			Columns []string `json:"columns"`
			Values  [][]any  `json:"values"`
		} `json:"series"`
		Error string `json:"error"`
	} `json:"results"`
	Error string `json:"error"`
}

func influxRows(b []byte) (NativeQueryResult, error) {
	var response influxQueryResponse
	if err := json.Unmarshal(b, &response); err != nil {
		return NativeQueryResult{}, err
	}
	if response.Error != "" {
		return NativeQueryResult{}, fmt.Errorf("influxdb query failed: %s", boundedNativeString(response.Error))
	}
	result := NativeQueryResult{}
	for _, statement := range response.Results {
		if statement.Error != "" {
			return result, fmt.Errorf("influxdb query failed: %s", boundedNativeString(statement.Error))
		}
		for _, series := range statement.Series {
			if len(result.Columns) == 0 {
				result.Columns = make([]string, len(series.Columns))
				for i, column := range series.Columns {
					result.Columns[i] = boundedNativeString(column)
				}
			}
			for _, values := range series.Values {
				if len(result.Rows) >= nativeMaxRows {
					result.Info = map[string]string{"truncated": "true"}
					return result, nil
				}
				row := make([]string, len(values))
				for i, v := range values {
					row[i] = boundedNativeString(fmt.Sprint(v))
				}
				result.Rows = append(result.Rows, row)
			}
		}
	}
	return result, nil
}
func (e *InfluxDBExecutor) influxQL(ctx context.Context, stmt, db string) (NativeQueryResult, error) {
	params := url.Values{"q": {stmt}}
	if db != "" {
		params.Set("db", db)
	}
	b, err := e.request(ctx, "/query", params)
	if err != nil {
		return NativeQueryResult{}, err
	}
	return influxRows(b)
}
func (e *InfluxDBExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	c := NativeCatalog{Category: model.CategoryTimeSeries, ServerVersion: v}
	result, err := e.influxQL(ctx, "SHOW DATABASES", "")
	if err != nil || len(result.Rows) == 0 {
		// InfluxDB 2 deployments without DBRP mappings can still expose buckets.
		b, x := e.request(ctx, "/api/v2/buckets", url.Values{"org": {e.org}, "limit": {"100"}})
		if x != nil {
			return c, x
		}
		var parsed struct {
			Buckets []struct {
				Name string `json:"name"`
			} `json:"buckets"`
		}
		if x = json.Unmarshal(b, &parsed); x != nil {
			return c, x
		}
		for _, bucket := range parsed.Buckets {
			c.Namespaces = append(c.Namespaces, NativeNamespace{Name: boundedNativeString(bucket.Name), Kind: "bucket", Metadata: map[string]string{"measurements": "unavailable without InfluxQL DBRP mapping", "counts": "unavailable"}})
		}
		return c, nil
	}
	for _, row := range result.Rows {
		if len(row) == 0 || len(c.Namespaces) >= nativeMaxRows {
			break
		}
		db := row[0]
		c.Namespaces = append(c.Namespaces, NativeNamespace{Name: db, Kind: "database", Metadata: map[string]string{"counts": "unavailable"}})
		if !influxIdentifier.MatchString(db) {
			continue
		}
		measurements, x := e.influxQL(ctx, "SHOW MEASUREMENTS", db)
		if x != nil {
			c.Namespaces[len(c.Namespaces)-1].Metadata["measurements"] = "unavailable"
			continue
		}
		for _, m := range measurements.Rows {
			if len(m) > 0 && len(c.Namespaces) < nativeMaxRows {
				c.Namespaces = append(c.Namespaces, NativeNamespace{Name: m[0], Kind: "measurement", Metadata: map[string]string{"database": db, "counts": "unavailable"}})
			}
		}
	}
	return c, nil
}

var influxIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z_0-9-]*$`)
var influxLimit = regexp.MustCompile(`(?i)\bLIMIT\s+([0-9]+)\s*$`)
var influxShow = regexp.MustCompile(`(?i)^SHOW\s+(DATABASES|MEASUREMENTS|TAG KEYS|FIELD KEYS|RETENTION POLICIES)(?:\s+ON\s+[a-zA-Z_][a-zA-Z_0-9-]*)?$`)
var influxFlux = regexp.MustCompile(`(?is)^from\s*\(\s*bucket\s*:\s*"[a-zA-Z_][a-zA-Z_0-9-]*"\s*\)\s*\|>\s*range\s*\(\s*start\s*:\s*-([0-9]+)([smhdw])\s*\)\s*\|>\s*limit\s*\(\s*n\s*:\s*([0-9]+)\s*\)$`)
var influxDelete = regexp.MustCompile(`(?i)^DELETE\s+FROM\s+[a-zA-Z_][a-zA-Z_0-9-]*\s+WHERE\s+time\s*>=\s*'([^']+)'\s+AND\s+time\s*<\s*'([^']+)'$`)

func influxStatementAllowed(command, stmt string, readOnly bool) error {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" || len(stmt) > nativeMaxValueBytes || strings.ContainsAny(stmt, ";\r\n") || strings.Contains(stmt, "--") || strings.Contains(stmt, "/*") {
		return fmt.Errorf("invalid or multiple influxdb statements")
	}
	fields := strings.Fields(stmt)
	if len(fields) == 0 {
		return fmt.Errorf("empty influxdb statement")
	}
	if command == "FLUX" || command == "FROM" {
		m := influxFlux.FindStringSubmatch(stmt)
		if m == nil {
			return fmt.Errorf("Flux requires one from/range/limit pipeline with limit 1..100")
		}
		window, _ := strconv.Atoi(m[1])
		unit := map[string]int{"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800}[strings.ToLower(m[2])]
		if window < 1 || window > 604800/unit {
			return fmt.Errorf("Flux range must be at most 7 days")
		}
		n, _ := strconv.Atoi(m[3])
		if n < 1 || n > 100 {
			return fmt.Errorf("Flux limit must be 1..100")
		}
		return nil
	}
	if !strings.EqualFold(fields[0], command) {
		return fmt.Errorf("influxdb command mismatch")
	}
	switch command {
	case "SHOW":
		if influxShow.MatchString(stmt) && !strings.EqualFold(stmt, "SHOW USERS") {
			return nil
		}
	case "SELECT":
		m := influxLimit.FindStringSubmatch(stmt)
		if m != nil {
			n, _ := strconv.Atoi(m[1])
			if n >= 1 && n <= 100 && strings.Contains(strings.ToUpper(stmt), " FROM ") && !strings.Contains(strings.ToUpper(stmt), " INTO ") {
				return nil
			}
		}
	case "EXPLAIN":
		if strings.HasPrefix(strings.ToUpper(stmt), "EXPLAIN SELECT ") {
			return influxStatementAllowed("SELECT", strings.TrimSpace(stmt[len("EXPLAIN "):]), readOnly)
		}
	case "WRITE":
		if !readOnly && regexp.MustCompile(`^[^\s,]+(?:,[^\s]+)?\s+[^\s=]+=[^\s]+\s+[0-9]+$`).MatchString(strings.TrimSpace(strings.TrimPrefix(stmt, "WRITE "))) {
			return nil
		}
	case "DELETE":
		m := influxDelete.FindStringSubmatch(stmt)
		if !readOnly && m != nil {
			start, e1 := time.Parse(time.RFC3339Nano, m[1])
			end, e2 := time.Parse(time.RFC3339Nano, m[2])
			if e1 == nil && e2 == nil && end.After(start) && end.Sub(start) <= 7*24*time.Hour {
				return nil
			}
		}
	}
	if readOnly && (command == "WRITE" || command == "INSERT" || command == "DELETE") {
		return fmt.Errorf("native write is forbidden in read-only mode")
	}
	return fmt.Errorf("influxdb statement is not allowed")
}
func influxCSV(raw string) (NativeQueryResult, error) {
	if len(raw) > 4<<20 {
		return NativeQueryResult{}, fmt.Errorf("Flux response exceeds size limit")
	}
	r := csv.NewReader(strings.NewReader(raw))
	r.FieldsPerRecord = -1
	var columns []string
	rows := make([][]string, 0)
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return NativeQueryResult{}, err
		}
		if len(record) == 0 || strings.HasPrefix(record[0], "#") {
			continue
		}
		if columns == nil {
			columns = make([]string, len(record))
			for i, value := range record {
				columns[i] = boundedNativeString(value)
			}
			continue
		}
		if len(record) == len(columns) && strings.Join(record, "\x00") == strings.Join(columns, "\x00") {
			continue
		}
		if len(rows) >= nativeMaxRows {
			out := boundedNativeRows(columns, rows)
			out.Info = map[string]string{"truncated": "true"}
			return out, nil
		}
		rows = append(rows, record)
	}
	return boundedNativeRows(columns, rows), nil
}
func (e *InfluxDBExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("influxdb", e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(request)
	if err != nil {
		return result, err
	}
	if len(args) != 1 {
		return result, fmt.Errorf("influxdb requires one statement")
	}
	stmt := strings.TrimSpace(args[0])
	if err = influxStatementAllowed(command, stmt, e.readOnly); err != nil {
		return result, err
	}
	if command == "FLUX" || command == "FROM" {
		raw, x := e.client.QueryAPI(e.org).QueryRaw(ctx, stmt, nil)
		if x != nil {
			return result, x
		}
		return influxCSV(raw)
	}
	if command == "WRITE" {
		if request.Namespace == "" || !influxIdentifier.MatchString(request.Namespace) {
			return result, fmt.Errorf("WRITE requires a bucket namespace")
		}
		line := strings.TrimSpace(strings.TrimPrefix(stmt, "WRITE "))
		if x := e.client.WriteAPIBlocking(e.org, request.Namespace).WriteRecord(ctx, line); x != nil {
			return result, x
		}
		return NativeQueryResult{Raw: "OK"}, nil
	}
	if command == "DELETE" && request.Namespace == "" {
		return result, fmt.Errorf("DELETE requires a mapped database namespace")
	}
	if request.Namespace != "" && !influxIdentifier.MatchString(request.Namespace) {
		return result, fmt.Errorf("invalid influxdb database namespace")
	}
	return e.influxQL(ctx, stmt, request.Namespace)
}
