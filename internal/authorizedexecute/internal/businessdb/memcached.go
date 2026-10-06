package businessdb

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/cuipengdba/agentsql/internal/model"
)

type MemcachedExecutor struct {
	client       *memcache.Client
	datasourceID string
	address      string
	timeout      time.Duration
	tlsConfig    *tls.Config
	readOnly     bool
}

func NewMemcachedExecutor(datasource model.Datasource, readOnly bool) (*MemcachedExecutor, error) {
	if strings.TrimSpace(datasource.Host) == "" {
		return nil, fmt.Errorf("memcached host is required")
	}
	port := datasource.Port
	if port == 0 {
		port = 11211
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("memcached port is invalid")
	}
	if datasource.Database != "" || datasource.Username != "" || datasource.TrustServerCertificate {
		return nil, fmt.Errorf("unsupported Memcached connection field")
	}
	timeoutMS, err := normalizedStatementTimeout(datasource.StmtTimeoutMS)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(datasource.Host, strconv.Itoa(port))
	timeout := time.Duration(timeoutMS) * time.Millisecond
	client := memcache.New(address)
	client.Timeout = timeout
	executor := &MemcachedExecutor{client: client, datasourceID: datasource.ID, address: address, timeout: timeout, readOnly: readOnly}
	if datasource.TLSMode != "" {
		if datasource.TLSMode != "strict" && datasource.TLSMode != "verify-full" {
			return nil, fmt.Errorf("invalid Memcached TLS mode")
		}
		config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: datasource.TLSServerName}
		if datasource.TLSCAFile != "" {
			pem, err := os.ReadFile(datasource.TLSCAFile)
			if err != nil {
				return nil, err
			}
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("invalid Memcached TLS CA")
			}
			config.RootCAs = roots
		}
		executor.tlsConfig = config
		client.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout}, Config: config}).DialContext(ctx, network, addr)
		}
	} else if datasource.TLSServerName != "" || datasource.TLSCAFile != "" {
		return nil, fmt.Errorf("Memcached TLS settings require tls_mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := executor.Ping(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return executor, nil
}

func (executor *MemcachedExecutor) Category() model.DatasourceCategory { return model.CategoryKeyValue }
func (executor *MemcachedExecutor) Dialect() model.DBDialect           { return model.DialectMemcached }
func (executor *MemcachedExecutor) Close() error                       { return executor.client.Close() }
func (executor *MemcachedExecutor) dial(ctx context.Context) (net.Conn, error) {
	if executor.tlsConfig != nil {
		return (&tls.Dialer{NetDialer: &net.Dialer{Timeout: executor.timeout}, Config: executor.tlsConfig}).DialContext(ctx, "tcp", executor.address)
	}
	return (&net.Dialer{Timeout: executor.timeout}).DialContext(ctx, "tcp", executor.address)
}
func (executor *MemcachedExecutor) textCommand(ctx context.Context, command string) ([]string, error) {
	conn, err := executor.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(executor.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if _, err = fmt.Fprintf(conn, "%s\r\n", command); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), nativeMaxValueBytes+1024)
	lines := make([]string, 0)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "CLIENT_ERROR") || strings.HasPrefix(line, "SERVER_ERROR") {
			return nil, fmt.Errorf("Memcached protocol error: %s", boundedNativeString(line))
		}
		if line == "END" {
			return lines, nil
		}
		lines = append(lines, boundedNativeString(line))
		if len(lines) >= nativeMaxRows {
			return nil, fmt.Errorf("Memcached response exceeds row limit")
		}
		if strings.HasPrefix(command, "version") {
			return lines, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("Memcached response ended unexpectedly")
}
func (executor *MemcachedExecutor) Ping(ctx context.Context) error {
	_, err := executor.ServerVersion(ctx)
	return err
}
func (executor *MemcachedExecutor) ServerVersion(ctx context.Context) (string, error) {
	lines, err := executor.textCommand(ctx, "version")
	if err != nil {
		return "", err
	}
	if len(lines) != 1 {
		return "", fmt.Errorf("invalid Memcached version response")
	}
	version, ok := strings.CutPrefix(lines[0], "VERSION ")
	if !ok {
		return "", fmt.Errorf("invalid Memcached version response")
	}
	return version, nil
}
func parseMemcachedStats(lines []string) map[string]map[string]string {
	stats := make(map[string]map[string]string)
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) != 3 || parts[0] != "STAT" {
			continue
		}
		segments := strings.Split(parts[1], ":")
		if len(segments) < 2 {
			continue
		}
		name, field := segments[0], segments[1]
		if len(segments) == 3 && segments[0] == "items" {
			name, field = segments[1], segments[2]
		}
		if stats[name] == nil {
			stats[name] = make(map[string]string)
		}
		stats[name][field] = boundedNativeString(parts[2])
	}
	return stats
}
func (executor *MemcachedExecutor) Discover(ctx context.Context) (NativeCatalog, error) {
	version, err := executor.ServerVersion(ctx)
	if err != nil {
		return NativeCatalog{}, err
	}
	items, err := executor.textCommand(ctx, "stats items")
	if err != nil {
		return NativeCatalog{}, err
	}
	slabs, err := executor.textCommand(ctx, "stats slabs")
	if err != nil {
		return NativeCatalog{}, err
	}
	itemStats, slabStats := parseMemcachedStats(items), parseMemcachedStats(slabs)
	namespaces := make([]NativeNamespace, 0)
	for slab, metadata := range slabStats {
		if slab == "active_slabs" || slab == "total_malloced" {
			continue
		}
		count, _ := strconv.ParseInt(itemStats[slab]["number"], 10, 64)
		namespaces = append(namespaces, NativeNamespace{Name: slab, Kind: "slab", ItemCount: count, Metadata: metadata})
		if len(namespaces) >= nativeMaxRows {
			break
		}
	}
	for slab, metadata := range itemStats {
		found := false
		for _, ns := range namespaces {
			if ns.Name == slab {
				found = true
				break
			}
		}
		if !found && len(namespaces) < nativeMaxRows {
			count, _ := strconv.ParseInt(metadata["number"], 10, 64)
			namespaces = append(namespaces, NativeNamespace{Name: slab, Kind: "slab", ItemCount: count, Metadata: metadata})
		}
	}
	return NativeCatalog{Category: model.CategoryKeyValue, ServerVersion: version, Namespaces: namespaces}, nil
}

var memcachedReadCommands = map[string]bool{"GET": true, "MGET": true, "STATS": true, "STATS ITEMS": true, "STATS SLABS": true, "VERSION": true}
var memcachedWriteCommands = map[string]bool{"SET": true, "ADD": true, "DELETE": true, "INCR": true, "DECR": true, "APPEND": true, "PREPEND": true}

func memcachedCommandAllowed(command string, readOnly bool) error {
	if memcachedReadCommands[command] {
		return nil
	}
	if memcachedWriteCommands[command] {
		if readOnly {
			return fmt.Errorf("native write command %s is forbidden in read-only mode", command)
		}
		return nil
	}
	return fmt.Errorf("native command %s is not allowed", command)
}
func (executor *MemcachedExecutor) NativeQuery(ctx context.Context, request NativeQueryRequest) (result NativeQueryResult, err error) {
	command := "INVALID"
	defer func() {
		auditNativeQuery(model.DialectMemcached, executor.datasourceID, command, executor.readOnly, err)
	}()
	var args []string
	command, args, err = normalizedNativeCommand(request)
	if err != nil {
		return NativeQueryResult{}, err
	}
	if err = memcachedCommandAllowed(command, executor.readOnly); err != nil {
		return NativeQueryResult{}, err
	}
	if request.Namespace != "" {
		return NativeQueryResult{}, fmt.Errorf("Memcached has no selectable namespace")
	}
	switch command {
	case "VERSION":
		version, err := executor.ServerVersion(ctx)
		if err != nil {
			return NativeQueryResult{}, err
		}
		return NativeQueryResult{Raw: version}, nil
	case "STATS", "STATS ITEMS", "STATS SLABS":
		if len(args) != 0 {
			return NativeQueryResult{}, fmt.Errorf("STATS takes no arguments")
		}
		lines, err := executor.textCommand(ctx, strings.ToLower(command))
		if err != nil {
			return NativeQueryResult{}, err
		}
		rows := make([][]string, 0, len(lines))
		for _, line := range lines {
			rows = append(rows, strings.Fields(line))
		}
		return boundedNativeRows([]string{"type", "name", "value"}, rows), nil
	case "GET":
		if len(args) != 1 {
			return NativeQueryResult{}, fmt.Errorf("GET needs one key")
		}
		item, err := executor.client.Get(args[0])
		if err == memcache.ErrCacheMiss {
			return NativeQueryResult{Columns: []string{"key", "value"}, Rows: [][]string{}}, nil
		}
		if err != nil {
			return NativeQueryResult{}, err
		}
		return boundedNativeRows([]string{"key", "value"}, [][]string{{item.Key, string(item.Value)}}), nil
	case "MGET":
		if len(args) == 0 {
			return NativeQueryResult{}, fmt.Errorf("MGET needs keys")
		}
		items, err := executor.client.GetMulti(args)
		if err != nil {
			return NativeQueryResult{}, err
		}
		rows := make([][]string, 0, len(items))
		for _, key := range args {
			if item := items[key]; item != nil {
				rows = append(rows, []string{key, string(item.Value)})
			}
		}
		return boundedNativeRows([]string{"key", "value"}, rows), nil
	case "SET", "ADD", "APPEND", "PREPEND":
		if len(args) != 2 {
			return NativeQueryResult{}, fmt.Errorf("%s needs key and value", command)
		}
		item := &memcache.Item{Key: args[0], Value: []byte(args[1])}
		switch command {
		case "SET":
			err = executor.client.Set(item)
		case "ADD":
			err = executor.client.Add(item)
		case "APPEND":
			err = executor.client.Append(item)
		case "PREPEND":
			err = executor.client.Prepend(item)
		}
	case "DELETE":
		if len(args) != 1 {
			return NativeQueryResult{}, fmt.Errorf("DELETE needs one key")
		}
		err = executor.client.Delete(args[0])
	case "INCR", "DECR":
		if len(args) != 2 {
			return NativeQueryResult{}, fmt.Errorf("%s needs key and delta", command)
		}
		delta, parseErr := strconv.ParseUint(args[1], 10, 64)
		if parseErr != nil {
			return NativeQueryResult{}, parseErr
		}
		var value uint64
		if command == "INCR" {
			value, err = executor.client.Increment(args[0], delta)
		} else {
			value, err = executor.client.Decrement(args[0], delta)
		}
		if err == nil {
			return NativeQueryResult{Raw: strconv.FormatUint(value, 10)}, nil
		}
	}
	if err != nil {
		return NativeQueryResult{}, err
	}
	return NativeQueryResult{Raw: "OK"}, nil
}
