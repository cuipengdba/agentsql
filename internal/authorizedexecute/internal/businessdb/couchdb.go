package businessdb

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"

	"github.com/cuipengdba/agentsql/internal/model"
)

type CouchDBExecutor struct {
	client                                 *http.Client
	base, username, password, datasourceID string
	readOnly                               bool
}

func couchDBURL(ds model.Datasource) (string, error) {
	if strings.TrimSpace(ds.Host) == "" {
		return "", fmt.Errorf("couchdb host is required")
	}
	port := ds.Port
	if port == 0 {
		port = 5984
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("couchdb port is invalid")
	}
	scheme := "http"
	if ds.TLSMode != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(ds.Host, strconv.Itoa(port)), nil
}
func NewCouchDBExecutor(ds model.Datasource, password string, readOnly bool) (*CouchDBExecutor, error) {
	if ds.DBType != "couchdb" {
		return nil, fmt.Errorf("invalid couchdb dialect")
	}
	base, err := couchDBURL(ds)
	if err != nil {
		return nil, err
	}
	timeout, err := normalizedStatementTimeout(ds.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	if ds.TrustServerCertificate {
		return nil, fmt.Errorf("unverified couchdb TLS is unsupported")
	}
	if ds.TLSMode != "" && ds.TLSMode != "strict" && ds.TLSMode != "verify-full" {
		return nil, fmt.Errorf("invalid couchdb TLS mode")
	}
	if ds.TLSMode == "" && (ds.TLSServerName != "" || ds.TLSCAFile != "") {
		return nil, fmt.Errorf("couchdb TLS settings require tls_mode")
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
				return nil, fmt.Errorf("invalid couchdb TLS CA")
			}
			cfg.RootCAs = roots
		}
		transport.TLSClientConfig = cfg
	}
	e := &CouchDBExecutor{client: &http.Client{Transport: transport, Timeout: time.Duration(timeout) * time.Millisecond}, base: base, username: ds.Username, password: password, datasourceID: ds.ID, readOnly: readOnly}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Millisecond)
	defer cancel()
	if err := e.Ping(ctx); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}
func (e *CouchDBExecutor) Category() model.DatasourceCategory { return model.CategoryDocument }
func (e *CouchDBExecutor) Dialect() model.DBDialect           { return "couchdb" }
func (e *CouchDBExecutor) Close() error                       { e.client.CloseIdleConnections(); return nil }
func (e *CouchDBExecutor) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, method, e.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if e.username != "" {
		r.SetBasicAuth(e.username, e.password)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("couchdb response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("couchdb HTTP %d", resp.StatusCode)
	}
	return b, nil
}
func (e *CouchDBExecutor) Ping(ctx context.Context) error {
	_, err := e.request(ctx, "GET", "/", nil)
	return err
}
func parseCouchDBVersion(b []byte) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	if v.Version == "" {
		return "", fmt.Errorf("couchdb version unavailable")
	}
	return boundedNativeString(v.Version), nil
}
func (e *CouchDBExecutor) ServerVersion(ctx context.Context) (string, error) {
	b, err := e.request(ctx, "GET", "/", nil)
	if err != nil {
		return "", err
	}
	return parseCouchDBVersion(b)
}
func (e *CouchDBExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	v, err := e.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	b, err := e.request(ctx, "GET", "/_all_dbs", nil)
	if err != nil {
		return NativeCatalog{}, err
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return NativeCatalog{}, err
	}
	cat := NativeCatalog{Category: model.CategoryDocument, ServerVersion: v}
	for _, name := range names {
		if len(cat.Namespaces) >= nativeMaxRows {
			break
		}
		b, err := e.request(ctx, "GET", "/"+url.PathEscape(name), nil)
		if err != nil {
			return NativeCatalog{}, err
		}
		var info struct {
			DocCount int64 `json:"doc_count"`
		}
		if err := json.Unmarshal(b, &info); err != nil {
			return NativeCatalog{}, err
		}
		cat.Namespaces = append(cat.Namespaces, NativeNamespace{Name: name, Kind: "database", ItemCount: info.DocCount})
	}
	return cat, nil
}
func couchDBID(s string) error {
	if s == "" || strings.HasPrefix(s, "_") || strings.ContainsAny(s, "\x00/?#") || s == "." || s == ".." {
		return fmt.Errorf("couchdb document ID is invalid")
	}
	return nil
}
func couchDBDatabase(s string) error {
	if s == "" || strings.HasPrefix(s, "_") || strings.ContainsAny(s, "\x00/?#") || s == "." || s == ".." {
		return fmt.Errorf("couchdb database is invalid")
	}
	return nil
}
func couchDBCommandAllowed(command string, readOnly bool) error {
	switch command {
	case "GET", "ALL_DOCS", "FIND", "VIEW":
		return nil
	case "PUT", "DELETE", "BULK_DOCS":
		if readOnly {
			return fmt.Errorf("couchdb write forbidden in read-only mode")
		}
		return nil
	default:
		return fmt.Errorf("couchdb command %s is not allowed", command)
	}
}
func couchDBDocument(b []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if !json.Valid(b) || json.Unmarshal(b, &m) != nil || m == nil {
		return nil, fmt.Errorf("couchdb document must be JSON object")
	}
	if _, ok := m["_deleted"]; ok {
		return nil, fmt.Errorf("couchdb deletion marker is not allowed")
	}
	return m, nil
}
func couchDBRows(b []byte) (NativeQueryResult, error) {
	var payload struct {
		Rows []json.RawMessage `json:"rows"`
		Docs []json.RawMessage `json:"docs"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return NativeQueryResult{}, err
	}
	rows := payload.Rows
	if rows == nil {
		rows = payload.Docs
	}
	if rows == nil {
		return NativeQueryResult{Raw: boundedNativeString(string(b))}, nil
	}
	out := [][]string{}
	for _, v := range rows {
		out = append(out, []string{string(v)})
	}
	return boundedNativeRows([]string{"document"}, out), nil
}
func (e *CouchDBExecutor) NativeQuery(ctx context.Context, req NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() { auditNativeQuery(e.Dialect(), e.datasourceID, command, e.readOnly, err) }()
	command, args, err := normalizedNativeCommand(req)
	if err != nil {
		return result, err
	}
	if err = couchDBCommandAllowed(command, e.readOnly); err != nil {
		return result, err
	}
	if err = couchDBDatabase(req.Namespace); err != nil {
		return result, err
	}
	base := "/" + url.PathEscape(req.Namespace)
	var method, path string
	var body []byte
	switch command {
	case "GET":
		if len(args) != 1 {
			return result, fmt.Errorf("couchdb GET requires ID")
		}
		if err = couchDBID(args[0]); err != nil {
			return result, err
		}
		method, path = "GET", base+"/"+url.PathEscape(args[0])
	case "ALL_DOCS":
		limit := nativeMaxRows
		if len(args) > 1 {
			return result, fmt.Errorf("invalid all_docs arguments")
		}
		if len(args) == 1 {
			limit, err = strconv.Atoi(args[0])
			if err != nil || limit < 1 || limit > nativeMaxRows {
				return result, fmt.Errorf("invalid all_docs limit")
			}
		}
		method, path = "GET", base+"/_all_docs?include_docs=true&limit="+strconv.Itoa(limit)
	case "FIND":
		if len(args) != 1 {
			return result, fmt.Errorf("couchdb FIND requires selector JSON")
		}
		var p map[string]json.RawMessage
		if json.Unmarshal([]byte(args[0]), &p) != nil || p == nil || len(p) != 1 || p["selector"] == nil {
			return result, fmt.Errorf("couchdb FIND accepts selector only")
		}
		var selector map[string]json.RawMessage
		if json.Unmarshal(p["selector"], &selector) != nil || len(selector) == 0 {
			return result, fmt.Errorf("invalid couchdb selector")
		}
		p["limit"] = json.RawMessage(strconv.Itoa(nativeMaxRows))
		body, err = json.Marshal(p)
		if err != nil {
			return result, err
		}
		method, path = "POST", base+"/_find"
	case "VIEW":
		if len(args) != 2 {
			return result, fmt.Errorf("couchdb VIEW requires design and view")
		}
		if err = couchDBID(args[0]); err != nil {
			return result, err
		}
		if err = couchDBID(args[1]); err != nil {
			return result, err
		}
		method, path = "GET", base+"/_design/"+url.PathEscape(args[0])+"/_view/"+url.PathEscape(args[1])+"?reduce=false&limit=1000"
	case "PUT":
		if len(args) != 2 {
			return result, fmt.Errorf("couchdb PUT requires ID and document")
		}
		if err = couchDBID(args[0]); err != nil {
			return result, err
		}
		doc, x := couchDBDocument([]byte(args[1]))
		if x != nil {
			return result, x
		}
		if id, ok := doc["_id"]; ok {
			var text string
			if json.Unmarshal(id, &text) != nil || text != args[0] {
				return result, fmt.Errorf("couchdb _id mismatch")
			}
		}
		body = []byte(args[1])
		method, path = "PUT", base+"/"+url.PathEscape(args[0])
	case "DELETE":
		if len(args) != 2 {
			return result, fmt.Errorf("couchdb DELETE requires ID and revision")
		}
		if err = couchDBID(args[0]); err != nil {
			return result, err
		}
		if args[1] == "" {
			return result, fmt.Errorf("couchdb revision required")
		}
		method, path = "DELETE", base+"/"+url.PathEscape(args[0])+"?rev="+url.QueryEscape(args[1])
	case "BULK_DOCS":
		if len(args) != 1 {
			return result, fmt.Errorf("couchdb BULK_DOCS requires JSON")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(args[0]), &fields) != nil || len(fields) < 1 || len(fields) > 2 {
			return result, fmt.Errorf("invalid couchdb bulk fields")
		}
		for name := range fields {
			if name != "docs" && name != "new_edits" {
				return result, fmt.Errorf("couchdb bulk field is not allowed")
			}
		}
		var p struct {
			Docs     []json.RawMessage `json:"docs"`
			NewEdits *bool             `json:"new_edits"`
		}
		if json.Unmarshal([]byte(args[0]), &p) != nil || len(p.Docs) == 0 || len(p.Docs) > nativeMaxRows || p.NewEdits != nil && !*p.NewEdits {
			return result, fmt.Errorf("invalid couchdb bulk documents")
		}
		for _, raw := range p.Docs {
			doc, x := couchDBDocument(raw)
			if x != nil {
				return result, x
			}
			if id, ok := doc["_id"]; ok {
				var text string
				if json.Unmarshal(id, &text) != nil || couchDBID(text) != nil {
					return result, fmt.Errorf("invalid couchdb bulk ID")
				}
			}
		}
		body = []byte(args[0])
		method, path = "POST", base+"/_bulk_docs"
	}
	b, err := e.request(ctx, method, path, body)
	if err != nil {
		return result, err
	}
	if command == "ALL_DOCS" || command == "FIND" || command == "VIEW" {
		return couchDBRows(b)
	}
	return NativeQueryResult{Raw: boundedNativeString(string(b))}, nil
}
