package businessdb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
)

type CouchbaseExecutor struct {
	client                                              *http.Client
	management, query, username, password, datasourceID string
	readOnly                                            bool
}

func couchbaseURLs(ds model.Datasource) (string, string, error) {
	if strings.TrimSpace(ds.Host) == "" {
		return "", "", fmt.Errorf("couchbase host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 11210
	}
	if port < 1 || port > 65535 {
		return "", "", fmt.Errorf("couchbase port is invalid")
	}
	management, query := port, port+2
	if port == 11210 {
		management, query = 8091, 8093
	}
	if ds.TLSMode != "" && port == 11210 {
		management, query = 18091, 18093
	}
	if query > 65535 {
		return "", "", fmt.Errorf("couchbase query port is invalid")
	}
	scheme := "http"
	if ds.TLSMode != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(management)), scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(query)), nil
}
func NewCouchbaseExecutor(ds model.Datasource, password string, readOnly bool) (*CouchbaseExecutor, error) {
	if ds.DBType != "couchbase" {
		return nil, fmt.Errorf("invalid couchbase dialect")
	}
	m, q, err := couchbaseURLs(ds)
	if err != nil {
		return nil, err
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	if ds.TrustServerCertificate {
		return nil, fmt.Errorf("unverified couchbase TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
		return nil, fmt.Errorf("invalid couchbase TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return nil, fmt.Errorf("couchbase TLS settings require tls_mode")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ds.TLSMode != "" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ds.TLSServerName}
		if ds.TLSCAFile != "" {
			pem, x := os.ReadFile(ds.TLSCAFile)
			if x != nil {
				return nil, x
			}
			roots, x := x509.SystemCertPool()
			if x != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid couchbase TLS CA")
			}
			cfg.RootCAs = roots
		}
		transport.TLSClientConfig = cfg
	}
	e := &CouchbaseExecutor{client: &http.Client{Transport: transport, Timeout: time.Duration(timeout) * time.Millisecond}, management: m, query: q, username: ds.Username, password: password, datasourceID: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}
func (e *CouchbaseExecutor) Category() model.DatasourceCategory { return model.CategoryDocument }
func (e *CouchbaseExecutor) Dialect() model.DBDialect           { return "couchbase" }
func (e *CouchbaseExecutor) Close() error                       { e.client.CloseIdleConnections(); return nil }
func (e *CouchbaseExecutor) request(ctx context.Context, method, endpoint string, body io.Reader, contentType string) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if e.username != "" {
		r.SetBasicAuth(e.username, e.password)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	resp, err := e.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("couchbase response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("couchbase HTTP %d", resp.StatusCode)
	}
	return b, nil
}
func (e *CouchbaseExecutor) Ping(ctx context.Context) error {
	_, err := e.request(ctx, "GET", e.management+"/pools", nil, "")
	return err
}
func parseCouchbaseVersion(b []byte) (string, error) {
	var v struct {
		ImplementationVersion string `json:"implementationVersion"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	if v.ImplementationVersion == "" {
		return "", fmt.Errorf("couchbase version unavailable")
	}
	return boundedNativeString(v.ImplementationVersion), nil
}
func (e *CouchbaseExecutor) ServerVersion(ctx context.Context) (string, error) {
	b, err := e.request(ctx, "GET", e.management+"/pools/default", nil, "")
	if err != nil {
		return "", err
	}
	if version, err := parseCouchbaseVersion(b); err == nil {
		return version, nil
	}
	b, err = e.request(ctx, "GET", e.management+"/pools", nil, "")
	if err != nil {
		return "", err
	}
	return parseCouchbaseVersion(b)
}

type couchbaseBucket struct {
	Name         string `json:"name"`
	FlushEnabled *bool  `json:"flushEnabled"`
	RamQuotaMB   *int64 `json:"ramQuotaMB"`
	Quota        struct {
		Ram int64 `json:"ram"`
	} `json:"quota"`
}

func parseCouchbaseBuckets(b []byte) ([]couchbaseBucket, error) {
	var buckets []couchbaseBucket
	err := json.Unmarshal(b, &buckets)
	return buckets, err
}
func (e *CouchbaseExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	b, err := e.request(ctx, "GET", e.management+"/pools/default/buckets", nil, "")
	if err != nil {
		return NativeCatalog{}, err
	}
	buckets, err := parseCouchbaseBuckets(b)
	if err != nil {
		return NativeCatalog{}, err
	}
	cat := NativeCatalog{Category: model.CategoryDocument, ServerVersion: v}
	for _, bucket := range buckets {
		if len(cat.Namespaces) >= nativeMaxRows {
			break
		}
		metadata := map[string]string{}
		if bucket.FlushEnabled != nil {
			metadata["flushEnabled"] = strconv.FormatBool(*bucket.FlushEnabled)
		} else {
			metadata["flushEnabled"] = "unavailable"
		}
		if bucket.RamQuotaMB != nil {
			metadata["ramQuotaMB"] = strconv.FormatInt(*bucket.RamQuotaMB, 10)
		} else if bucket.Quota.Ram > 0 {
			metadata["ramQuotaMB"] = strconv.FormatInt(bucket.Quota.Ram/(1024*1024), 10)
		} else {
			metadata["ramQuotaMB"] = "unavailable"
		}
		entry := NativeNamespace{Name: bucket.Name, Kind: "bucket", Metadata: metadata}
		cat.Namespaces = append(cat.Namespaces, entry)
		body, x := e.request(ctx, "GET", e.management+"/pools/default/buckets/"+url.PathEscape(bucket.Name)+"/scopes", nil, "")
		if x != nil {
			cat.Namespaces[len(cat.Namespaces)-1].Metadata["scopesDiscovery"] = "unavailable"
			continue
		}
		var scopes struct {
			Scopes []struct {
				Name        string `json:"name"`
				Collections []struct {
					Name string `json:"name"`
				} `json:"collections"`
			} `json:"scopes"`
		}
		if json.Unmarshal(body, &scopes) != nil {
			cat.Namespaces[len(cat.Namespaces)-1].Metadata["scopesDiscovery"] = "unavailable"
			continue
		}
		for _, scope := range scopes.Scopes {
			if len(cat.Namespaces) >= nativeMaxRows {
				break
			}
			cat.Namespaces = append(cat.Namespaces, NativeNamespace{Name: bucket.Name + "." + scope.Name, Kind: "scope", Metadata: map[string]string{"bucket": bucket.Name}})
			for _, collection := range scope.Collections {
				if len(cat.Namespaces) >= nativeMaxRows {
					break
				}
				cat.Namespaces = append(cat.Namespaces, NativeNamespace{Name: bucket.Name + "." + scope.Name + "." + collection.Name, Kind: "collection", Metadata: map[string]string{"bucket": bucket.Name, "scope": scope.Name}})
			}
		}
	}
	return cat, nil
}

var couchbaseWord = regexp.MustCompile(`[A-Za-z_]+`)
var couchbaseLimit = regexp.MustCompile(`(?i)\bLIMIT\s+([0-9]+)\b`)
var couchbaseInsert = regexp.MustCompile(`(?is)^(?:INSERT|UPSERT)\s+INTO\s+[A-Za-z_][A-Za-z0-9_-]*\s*\(\s*KEY\s*,\s*VALUE\s*\)\s+VALUES\s*\(\s*"[^"\r\n]+"\s*,\s*(\{.*\})\s*\)$`)
var couchbaseUpdate = regexp.MustCompile(`(?is)^UPDATE\s+[A-Za-z_][A-Za-z0-9_-]*\s+USE\s+KEYS\s+"[^"\r\n]+"\s+SET\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*(.+)$`)
var couchbaseDelete = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+[A-Za-z_][A-Za-z0-9_-]*\s+USE\s+KEYS\s+"[^"\r\n]+"$`)

func couchbaseStatementAllowed(command, statement string, readOnly bool) (string, error) {
	command = strings.ToUpper(command)
	s := strings.TrimSpace(statement)
	if s == "" || len(s) > nativeMaxValueBytes || strings.ContainsAny(s, ";\\") || strings.Contains(s, "--") || strings.Contains(s, "/*") || strings.Contains(s, "*/") {
		return "", fmt.Errorf("unsafe N1QL statement")
	}
	// Strip literals and quoted identifiers before looking for executable keywords.
	var clean strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			clean.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			clean.WriteByte(' ')
			continue
		}
		clean.WriteByte(c)
	}
	if quote != 0 {
		return "", fmt.Errorf("unterminated N1QL literal")
	}
	words := couchbaseWord.FindAllString(strings.ToUpper(clean.String()), -1)
	if len(words) == 0 || words[0] != command {
		return "", fmt.Errorf("N1QL command mismatch")
	}
	for _, w := range words {
		if w == "CREATE" || w == "DROP" || w == "ALTER" || w == "GRANT" || w == "REVOKE" || w == "ADMIN" || w == "EXPLAIN" || w == "EXECUTE" || w == "PREPARE" || w == "MERGE" || w == "INFER" || w == "BUILD" || w == "INDEX" || w == "RETURNING" {
			return "", fmt.Errorf("N1QL operation %s is not allowed", w)
		}
	}
	if command == "SELECT" {
		if strings.ContainsAny(clean.String(), "()") {
			return "", fmt.Errorf("N1QL SELECT expression is unsupported")
		}
		for _, w := range words {
			if w == "INSERT" || w == "UPDATE" || w == "DELETE" || w == "UPSERT" || w == "CALL" || w == "FUNCTION" || w == "LET" || w == "WITH" || w == "UNION" || w == "INTERSECT" || w == "EXCEPT" {
				return "", fmt.Errorf("N1QL write operation rejected")
			}
		}
		if match := couchbaseLimit.FindStringSubmatch(clean.String()); match != nil {
			n, _ := strconv.Atoi(match[1])
			if n < 1 || n > nativeMaxRows {
				return "", fmt.Errorf("N1QL limit exceeds cap")
			}
			return s, nil
		}
		return s + " LIMIT 1000", nil
	}
	if readOnly {
		return "", fmt.Errorf("N1QL write forbidden in read-only mode")
	}
	if command != "INSERT" && command != "UPSERT" && command != "UPDATE" && command != "DELETE" {
		return "", fmt.Errorf("N1QL command is not allowed")
	}
	switch command {
	case "INSERT", "UPSERT":
		match := couchbaseInsert.FindStringSubmatch(s)
		if match == nil || !json.Valid([]byte(match[1])) {
			return "", fmt.Errorf("N1QL insert requires a simple KEY and JSON VALUE")
		}
	case "UPDATE":
		match := couchbaseUpdate.FindStringSubmatch(s)
		if match == nil || !json.Valid([]byte(strings.TrimSpace(match[1]))) {
			return "", fmt.Errorf("N1QL update requires USE KEYS and a JSON value")
		}
	case "DELETE":
		if !couchbaseDelete.MatchString(s) {
			return "", fmt.Errorf("N1QL delete requires USE KEYS")
		}
	}
	return s, nil
}
func (e *CouchbaseExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.Dialect(), e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if len(args) != 1 {
		return result, fmt.Errorf("N1QL statement argument required")
	}
	statement, err := couchbaseStatementAllowed(command, args[0], e.readOnly)
	if err != nil {
		return result, err
	}
	form := url.Values{"statement": {statement}}
	if e.readOnly {
		form.Set("readonly", "true")
	}
	b, err := e.request(ctx, "POST", e.query+"/query/service", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return result, err
	}
	var response struct {
		Status  string            `json:"status"`
		Results []json.RawMessage `json:"results"`
		Errors  []json.RawMessage `json:"errors"`
	}
	if err = json.Unmarshal(b, &response); err != nil {
		return result, err
	}
	if response.Status != "success" {
		return result, fmt.Errorf("couchbase query failed")
	}
	rows := [][]string{}
	for _, row := range response.Results {
		rows = append(rows, []string{string(row)})
	}
	return boundedNativeRows([]string{"document"}, rows), nil
}
