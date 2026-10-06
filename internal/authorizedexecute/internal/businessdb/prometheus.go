package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

type PrometheusExecutor struct {
	client                                 *http.Client
	base, username, password, datasourceID string
}

func prometheusOptions(ds model.Datasource, password string) (string, *http.Client, error) {
	if ds.DBType != "prometheus" || strings.TrimSpace(ds.Host) == "" {
		return "", nil, fmt.Errorf("prometheus host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 9090
	}
	if port < 1 || port > 65535 {
		return "", nil, fmt.Errorf("prometheus port is invalid")
	}
	if ds.Username == "" && password != "" {
		return "", nil, fmt.Errorf("prometheus password requires username")
	}
	if ds.TrustServerCertificate {
		return "", nil, fmt.Errorf("unverified prometheus TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
		return "", nil, fmt.Errorf("invalid prometheus TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return "", nil, fmt.Errorf("prometheus TLS settings require tls_mode")
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
				return "", nil, fmt.Errorf("invalid prometheus TLS CA")
			}
			cfg.RootCAs = roots
		}
		transport.TLSClientConfig = cfg
	}
	return scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(port)), &http.Client{Transport: transport, Timeout: time.Duration(timeout) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func NewPrometheusExecutor(ds model.Datasource, password string, _ bool) (*PrometheusExecutor, error) {
	base, client, err := prometheusOptions(ds, password)
	if err != nil {
		return nil, err
	}
	e := &PrometheusExecutor{client: client, base: base, username: ds.Username, password: password, datasourceID: ds.ID}
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}
func (*PrometheusExecutor) Category() model.DatasourceCategory { return model.CategoryTimeSeries }
func (*PrometheusExecutor) Dialect() model.DBDialect           { return "prometheus" }
func (e *PrometheusExecutor) Close() error                     { e.client.CloseIdleConnections(); return nil }

type prometheusEnvelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
	Error  string          `json:"error"`
}

func (e *PrometheusExecutor) get(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	u := e.base + "/api/v1/" + path
	if params != nil {
		u += "?" + params.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if e.username != "" {
		r.SetBasicAuth(e.username, e.password)
	}
	resp, err := e.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("prometheus response exceeds size limit")
	}
	var envelope prometheusEnvelope
	if err := json.Unmarshal(b, &envelope); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK || envelope.Status != "success" {
		return nil, fmt.Errorf("prometheus API rejected request: %s", boundedNativeString(envelope.Error))
	}
	return envelope.Data, nil
}
func (e *PrometheusExecutor) Ping(ctx context.Context) error {
	_, err := e.ServerVersion(ctx)
	return err
}
func (e *PrometheusExecutor) ServerVersion(ctx context.Context) (string, error) {
	b, err := e.get(ctx, "status/buildinfo", nil)
	if err != nil {
		return "", err
	}
	var data struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return "", err
	}
	if data.Version == "" {
		return "", fmt.Errorf("prometheus version unavailable")
	}
	return boundedNativeString(data.Version), nil
}
func (e *PrometheusExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	b, err := e.get(ctx, "labels", nil)
	if err != nil {
		return NativeCatalog{}, err
	}
	var labels []string
	if err := json.Unmarshal(b, &labels); err != nil {
		return NativeCatalog{}, err
	}
	result := NativeCatalog{Category: model.CategoryTimeSeries, ServerVersion: v}
	for _, label := range labels {
		if len(result.Namespaces) >= nativeMaxRows {
			break
		}
		if !prometheusLabel.MatchString(label) {
			continue
		}
		values, err := e.get(ctx, "label/"+label+"/values", nil)
		meta := map[string]string{"counts": "label value count only; series counts unavailable"}
		var names []string
		if err == nil {
			err = json.Unmarshal(values, &names)
		}
		if err != nil {
			meta["values"] = "unavailable"
		} else {
			meta["values"] = strconv.Itoa(len(names))
		}
		result.Namespaces = append(result.Namespaces, NativeNamespace{Name: boundedNativeString(label), Kind: "label", Metadata: meta})
		for _, name := range names {
			if len(result.Namespaces) >= nativeMaxRows {
				break
			}
			result.Namespaces = append(result.Namespaces, NativeNamespace{Name: boundedNativeString(name), Kind: "label_value", Metadata: map[string]string{"label": label}})
		}
	}
	return result, nil
}

var prometheusLabel = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z_0-9]*$`)
var prometheusMetric = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z_:0-9]*$`)
var prometheusTop = regexp.MustCompile(`(?i)^(?:topk|bottomk)\s*\(\s*([0-9]+)\s*,`)
var prometheusAllMetrics = regexp.MustCompile(`(?i)\{[^}]*__name__\s*=~`)
var prometheusSimple = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z_:0-9]*$|^vector\([0-9]+(?:\.[0-9]+)?\)$`)

func prometheusQueryAllowed(query string) error {
	q := strings.TrimSpace(query)
	if q == "" || len(q) > nativeMaxValueBytes || strings.ContainsAny(q, ";\r\n") {
		return fmt.Errorf("invalid or multiple PromQL queries")
	}
	if prometheusAllMetrics.MatchString(q) {
		return fmt.Errorf("unbounded all-metric selector is forbidden")
	}
	if prometheusSimple.MatchString(q) {
		return nil
	}
	m := prometheusTop.FindStringSubmatch(q)
	if m == nil {
		return fmt.Errorf("PromQL query requires topk/bottomk with bound 1..100")
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 || n > 100 {
		return fmt.Errorf("PromQL topk/bottomk bound must be 1..100")
	}
	if strings.Contains(q, "@") {
		return fmt.Errorf("PromQL @ modifier is unsupported")
	}
	depth := 0
	for i, ch := range q {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 || depth == 0 && i != len(q)-1 {
				return fmt.Errorf("PromQL topk/bottomk must bound the entire expression")
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("unbalanced PromQL expression")
	}
	return nil
}
func prometheusRangeAllowed(start, end, step string) error {
	a, err := strconv.ParseFloat(start, 64)
	if err != nil {
		return err
	}
	b, err := strconv.ParseFloat(end, 64)
	if err != nil {
		return err
	}
	s, err := strconv.ParseFloat(step, 64)
	if err != nil {
		return err
	}
	if math.IsNaN(a) || math.IsNaN(b) || math.IsNaN(s) || math.IsInf(a, 0) || math.IsInf(b, 0) || math.IsInf(s, 0) || s <= 0 || b <= a || b-a > 7*24*3600 || (b-a)/s > nativeMaxRows {
		return fmt.Errorf("query_range exceeds time or sample limit")
	}
	return nil
}
func prometheusSeriesWindowAllowed(start, end string) error {
	a, err := strconv.ParseFloat(start, 64)
	if err != nil {
		return err
	}
	b, err := strconv.ParseFloat(end, 64)
	if err != nil {
		return err
	}
	if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) || b <= a || b-a > 7*24*3600 {
		return fmt.Errorf("series time range must be at most 7 days")
	}
	return nil
}
func prometheusResult(b json.RawMessage) (NativeQueryResult, error) {
	var data any
	if err := json.Unmarshal(b, &data); err != nil {
		return NativeQueryResult{}, err
	}
	var values []any
	if m, ok := data.(map[string]any); ok {
		if x, ok := m["result"].([]any); ok {
			values = x
		} else {
			values = []any{m}
		}
	} else if x, ok := data.([]any); ok {
		values = x
	} else {
		values = []any{data}
	}
	rows := make([][]string, 0, min(len(values), nativeMaxRows))
	for _, value := range values {
		if len(rows) >= nativeMaxRows {
			break
		}
		raw, _ := json.Marshal(value)
		rows = append(rows, []string{string(raw)})
	}
	r := boundedNativeRows([]string{"value"}, rows)
	if len(values) > nativeMaxRows {
		r.Info = map[string]string{"truncated": "true"}
	}
	return r, nil
}
func (e *PrometheusExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery("prometheus", e.datasourceID, command, true, err) }()
	command, args, err := normalizedNativeCommand(request)
	if err != nil {
		return result, err
	}
	if request.Namespace != "" {
		return result, fmt.Errorf("prometheus namespace selection is unsupported")
	}
	params := url.Values{}
	path := ""
	switch command {
	case "QUERY":
		if len(args) != 1 {
			return result, fmt.Errorf("query requires one PromQL expression")
		}
		if err = prometheusQueryAllowed(args[0]); err != nil {
			return result, err
		}
		path = "query"
		params.Set("query", args[0])
	case "QUERY_RANGE":
		if len(args) != 4 {
			return result, fmt.Errorf("query_range requires query, start, end, step")
		}
		if err = prometheusQueryAllowed(args[0]); err != nil {
			return result, err
		}
		if err = prometheusRangeAllowed(args[1], args[2], args[3]); err != nil {
			return result, err
		}
		path = "query_range"
		params.Set("query", args[0])
		params.Set("start", args[1])
		params.Set("end", args[2])
		params.Set("step", args[3])
	case "SERIES":
		if len(args) != 3 || !prometheusMetric.MatchString(args[0]) {
			return result, fmt.Errorf("series requires metric name, start and end")
		}
		if err = prometheusSeriesWindowAllowed(args[1], args[2]); err != nil {
			return result, err
		}
		path = "series"
		params.Add("match[]", args[0])
		params.Set("start", args[1])
		params.Set("end", args[2])
	case "LABELS":
		if len(args) != 0 {
			return result, fmt.Errorf("labels takes no arguments")
		}
		path = "labels"
	case "LABEL_VALUES":
		if len(args) != 1 || !prometheusLabel.MatchString(args[0]) {
			return result, fmt.Errorf("label_values requires one label name")
		}
		path = "label/" + args[0] + "/values"
	default:
		if command == "WRITE" || command == "INSERT" || command == "DELETE" {
			return result, fmt.Errorf("prometheus 为只读数据源")
		}
		return result, fmt.Errorf("prometheus endpoint is not allowed")
	}
	b, err := e.get(ctx, path, params)
	if err != nil {
		return result, err
	}
	return prometheusResult(b)
}
