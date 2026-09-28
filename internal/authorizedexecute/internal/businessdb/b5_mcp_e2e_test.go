package businessdb_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	authorizedexecute "github.com/cuipengdba/agentsql/internal/authorizedexecute"
	"github.com/cuipengdba/agentsql/internal/b5"
	"github.com/cuipengdba/agentsql/internal/b5coordinator"
	"github.com/cuipengdba/agentsql/internal/b5dml"
	"github.com/cuipengdba/agentsql/internal/b5session"
	"github.com/cuipengdba/agentsql/internal/bootstrap"
	"github.com/cuipengdba/agentsql/internal/config"
	"github.com/cuipengdba/agentsql/internal/mcpserver"
	"github.com/cuipengdba/agentsql/internal/model"
	"github.com/cuipengdba/agentsql/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestB5MCPHTTPAndStdioPostgres14And18(t *testing.T) {
	if os.Getenv("AGENTSQL_B5_MCP_MATRIX") != "1" {
		t.Skip("set AGENTSQL_B5_MCP_MATRIX=1 to run the PG14/18 MCP matrix")
	}
	for _, major := range []int{14, 18} {
		t.Run("postgres-"+strconv.Itoa(major), func(t *testing.T) {
			runB5MCPMatrix(t, major)
		})
	}
}

func runB5MCPMatrix(t *testing.T, major int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	t.Cleanup(cancel)
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dbext", "postgres", "agentsql_binder"))
	require.NoError(t, err)
	majorText := strconv.Itoa(major)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "Dockerfile.test", Repo: "agentsql-binder-dml-test", Tag: "pg" + majorText, BuildArgs: map[string]*string{"PG_MAJOR": &majorText}, KeepImage: true},
			Env:            map[string]string{"POSTGRES_DB": "agentsql", "POSTGRES_USER": "agentsql", "POSTGRES_PASSWORD": "agentsql-password"},
			Entrypoint:     []string{"/bin/bash", "-c"},
			Cmd:            []string{`openssl req -new -x509 -nodes -days 1 -subj '/CN=localhost' -out /tmp/agentsql-server.crt -keyout /tmp/agentsql-server.key >/dev/null 2>&1 && chown postgres:postgres /tmp/agentsql-server.crt /tmp/agentsql-server.key && chmod 600 /tmp/agentsql-server.key && exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/agentsql-server.crt -c ssl_key_file=/tmp/agentsql-server.key`},
			Labels:         map[string]string{"agentsql.b5.mcp-matrix": "true"}, ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForAll(wait.ForListeningPort("5432/tcp"), wait.ForLog("database system is ready to accept connections").WithOccurrence(2)).WithDeadline(2 * time.Minute),
		}, Started: true,
	})
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	setup, err := pgx.Connect(ctx, fmt.Sprintf("postgres://agentsql:agentsql-password@%s:%d/agentsql?sslmode=disable", host, port.Int()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = setup.Close(context.Background()) })
	for _, statement := range []string{`CREATE SCHEMA agentsql_catalog`, `CREATE EXTENSION agentsql_binder WITH SCHEMA agentsql_catalog`, `CREATE SCHEMA s8`, `CREATE TABLE s8.items(id integer, value integer)`, `INSERT INTO s8.items VALUES (1,10),(2,20)`} {
		_, err = setup.Exec(ctx, statement)
		require.NoError(t, err, statement)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	cfg := config.Config{Server: config.ServerConfig{HTTPListen: "127.0.0.1:8650", EventStreamMaxConnections: 100}, Store: config.StoreConfig{SQLitePath: filepath.Join(t.TempDir(), "metadata.db"), AutoMigrate: true}, Defaults: config.DefaultsConfig{StatementTimeoutMS: 5_000, RowLimit: 1_000, MaxConnsPerDatasource: 5, QPSPerAgent: 100}, Theme: config.ThemeConfig{Default: "dark"}}
	runtime, err := bootstrap.Assemble(ctx, cfg, secret)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	apiKey, apiHash, err := store.GenerateAPIKey()
	require.NoError(t, err)
	agent, err := runtime.Store.Agents().Create(ctx, model.Agent{ID: "s8-agent", Name: "S8", Status: "active", APIKeyHash: apiHash, Level: "dml"})
	require.NoError(t, err)
	datasource := model.Datasource{ID: "s8-matrix", Name: "S8 Matrix", DBType: "postgres", Host: host, Port: port.Int(), Database: "agentsql", Username: "agentsql", ConnLimit: 5, StmtTimeoutMS: 5_000, RowLimit: 100}
	datasource, err = runtime.Store.Datasources().Create(ctx, datasource, "agentsql-password")
	require.NoError(t, err)
	_, err = runtime.Store.Policies().Create(ctx, model.Policy{ID: "s8-access", AgentID: agent.ID, DatasourceID: datasource.ID, ObjectType: "table", ObjectName: "s8.items", Action: "allow"})
	require.NoError(t, err)

	gateway := authorizedexecute.NewGateway(false)
	t.Cleanup(func() { require.NoError(t, gateway.CloseAll()) })
	pgRuntime, err := gateway.NewB5PostgresRuntimeWithPolicyProvider(ctx, datasource, secret, func(_ context.Context, facts b5dml.StatementFacts, _ b5coordinator.StatementRequest, _ int) (authorizedexecute.B5DMLAuthorizationConfig, error) {
		grants := []b5dml.Grant{{Element: b5dml.GrantAction, Action: facts.Action, Relation: facts.Target}}
		for _, write := range facts.Writes {
			grant := b5dml.Grant{Element: b5dml.GrantWriteTarget, Action: facts.Action, Relation: write.Relation, WriteKind: write.Kind}
			if write.Kind == b5dml.WriteTargetColumn {
				grant.Column = write.Column
			}
			grants = append(grants, grant)
		}
		for _, reference := range facts.References {
			grants = append(grants, b5dml.Grant{Element: b5dml.GrantReference, Action: facts.Action, Relation: reference.Relation, Column: reference.Column, ReferenceKind: reference.Kind})
		}
		return authorizedexecute.B5DMLAuthorizationConfig{PrincipalID: agent.ID, DatasourceID: datasource.ID, Policies: []b5dml.Policy{{ID: "s8-exact", Revision: 1, PrincipalID: agent.ID, DatasourceID: datasource.ID, Effect: b5dml.GrantAllow, Grants: grants}}, Decision: b5coordinator.DecisionAllow, PreliminaryAllowed: true, DatasourceSupported: true, PolicySnapshotDigest: "s8-exact-v1"}, nil
	})
	require.NoError(t, err)
	sealer, err := b5session.NewAESGCMSealer("s8-test", secret)
	require.NoError(t, err)
	directory, err := b5session.NewDirectory(b5session.DirectoryConfig{Store: runtime.Store.B5Sessions(), Sealer: sealer, IdleTTL: 15 * time.Second, AbsoluteTTL: 60 * time.Second})
	require.NoError(t, err)
	coordinator, err := b5coordinator.New(b5coordinator.Config{Transactions: runtime.Store.B5Transactions(), Sessions: b5coordinator.DirectorySessionGate{Directory: directory}, Engine: pgRuntime.Engine, Audit: &b5coordinator.MemoryAuditor{}})
	require.NoError(t, err)
	service := &mcpserver.B5CoordinatorService{Directory: directory, Coordinator: coordinator, Analyzer: pgRuntime.Analyzer, InstanceID: "s8-instance", StickyRoute: "s8-instance", FinalFence: func(context.Context, b5coordinator.Result) error { return nil },
		ResolveDatasource: func(context.Context, string) (mcpserver.B5DatasourceAuthority, error) {
			return mcpserver.B5DatasourceAuthority{Dialect: "postgres", Mode: "NATIVE_C_V1", ServerMajor: major, KeyRevision: 1, DatasourceRevision: 1, PolicyRevision: 1}, nil
		}}
	options := mcpserver.B5Options{B5Sessions: true, B5TxPostgres: true, Service: service}

	handler, err := mcpserver.NewHTTPHandler(runtime, cfg, zerolog.Nop(), mcpserver.WithB5Sessions(options))
	require.NoError(t, err)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	httpClient := &matrixHTTPClient{t: t, client: httpServer.Client(), url: httpServer.URL + "/mcp", apiKey: apiKey}
	runMCPTransaction(t, httpClient, major, "http", 1)

	stdioServer, err := mcpserver.NewServer(ctx, mcpserver.Options{APIKey: apiKey, Runtime: runtime, Logger: zerolog.Nop(), B5: options})
	require.NoError(t, err)
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	stdioErrors := make(chan error, 1)
	go func() { stdioErrors <- stdioServer.RunStdioStreams(ctx, serverInput, serverOutput) }()
	stdioClient := &matrixStdioClient{t: t, writer: clientInput, reader: clientOutput}
	stdioClient.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "s8-e2e", "version": "1"}})
	runMCPTransaction(t, stdioClient, major, "stdio", 2)
	require.NoError(t, clientInput.Close())
	select {
	case runErr := <-stdioErrors:
		require.NoError(t, runErr)
	case <-time.After(2 * time.Second):
		t.Fatal("stdio server did not stop after EOF")
	}

	var first, second int
	require.NoError(t, setup.QueryRow(ctx, `SELECT value FROM s8.items WHERE id=1`).Scan(&first))
	require.NoError(t, setup.QueryRow(ctx, `SELECT value FROM s8.items WHERE id=2`).Scan(&second))
	require.Equal(t, 13, first)
	require.Equal(t, 23, second)
}

type matrixCaller interface {
	tool(string, any) matrixToolResponse
}

type matrixToolResponse struct {
	Decision              string          `json:"decision"`
	ErrorCode             string          `json:"error_code"`
	DBOutcome             string          `json:"db_outcome"`
	ConnectionDisposition string          `json:"connection_disposition"`
	Data                  json.RawMessage `json:"data"`
}

func runMCPTransaction(t *testing.T, caller matrixCaller, major int, suffix string, increment int) {
	t.Helper()
	opened := caller.tool("open_session", mcpserver.B5OpenSessionInput{})
	require.Equal(t, "allow", opened.Decision)
	var session mcpserver.B5SessionView
	require.NoError(t, json.Unmarshal(opened.Data, &session))
	statements := []mcpserver.B5PlanStatement{{OperationID: "op-1", SQL: fmt.Sprintf("UPDATE s8.items SET value=value+%d WHERE id=1", increment), Reason: "S8 MCP matrix"}, {OperationID: "op-2", SQL: fmt.Sprintf("UPDATE s8.items SET value=value+%d WHERE id=2", increment), Reason: "S8 MCP matrix"}}
	begin := mcpserver.B5BeginInput{B5Continuation: mcpserver.B5Continuation{SessionID: session.SessionID, OwnerEpoch: session.OwnerEpoch, RequestID: "begin-" + suffix}, TransactionID: "tx-" + suffix + "-" + strconv.Itoa(major), DatasourceID: "s8-matrix", Dialect: "postgres", ServerMajor: major, KeyRevision: 1, DatasourceRevision: 1, PolicyRevision: 1, Statements: statements}
	signMatrix(t, session.ContinuationSecret, "begin_transaction", &begin.B5Continuation, begin)
	beginResponse := caller.tool("begin_transaction", begin)
	require.Equal(t, "allow", beginResponse.Decision, beginResponse.ErrorCode)
	for ordinal, statement := range statements {
		execute := mcpserver.B5ExecuteInput{B5Continuation: mcpserver.B5Continuation{SessionID: session.SessionID, OwnerEpoch: session.OwnerEpoch, RequestID: fmt.Sprintf("execute-%s-%d", suffix, ordinal)}, TransactionID: begin.TransactionID, OperationID: statement.OperationID, Ordinal: ordinal}
		signMatrix(t, session.ContinuationSecret, "execute_transaction_statement", &execute.B5Continuation, execute)
		require.Equal(t, "allow", caller.tool("execute_transaction_statement", execute).Decision)
	}
	commit := mcpserver.B5FinishInput{B5Continuation: mcpserver.B5Continuation{SessionID: session.SessionID, OwnerEpoch: session.OwnerEpoch, RequestID: "commit-" + suffix}, TransactionID: begin.TransactionID}
	signMatrix(t, session.ContinuationSecret, "commit_transaction", &commit.B5Continuation, commit)
	committed := caller.tool("commit_transaction", commit)
	if committed.Decision == "error" {
		require.Equal(t, string(b5.ErrorTxCommittedConnectionQuarantined), committed.ErrorCode)
		require.Equal(t, string(b5.OutcomeCommitted), committed.DBOutcome)
		require.Equal(t, string(b5.DispositionDiscarded), committed.ConnectionDisposition)
	} else {
		require.Equal(t, "allow", committed.Decision, committed.ErrorCode)
	}
}

func signMatrix(t *testing.T, secret, method string, continuation *mcpserver.B5Continuation, payload any) {
	t.Helper()
	digest, err := mcpserver.B5BodyDigest(method, payload)
	require.NoError(t, err)
	continuation.BodyDigest = hex.EncodeToString(digest[:])
	proof, err := b5session.SignContinuation(secret, continuation.SessionID, method, continuation.RequestID, continuation.OwnerEpoch, continuation.ExpectedSeq, digest)
	require.NoError(t, err)
	continuation.ContinuationProof = proof
}

type matrixHTTPClient struct {
	t           *testing.T
	client      *http.Client
	url, apiKey string
}

func (client *matrixHTTPClient) tool(name string, arguments any) matrixToolResponse {
	return client.call("tools/call", map[string]any{"name": name, "arguments": arguments})
}
func (client *matrixHTTPClient) call(method string, params any) matrixToolResponse {
	client.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": method, "method": method, "params": params})
	request, err := http.NewRequest(http.MethodPost, client.url, strings.NewReader(string(body)))
	require.NoError(client.t, err)
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response, err := client.client.Do(request)
	require.NoError(client.t, err)
	defer response.Body.Close()
	require.Equal(client.t, http.StatusOK, response.StatusCode)
	wire, err := io.ReadAll(response.Body)
	require.NoError(client.t, err)
	return decodeMatrixResponse(client.t, wire)
}

type matrixStdioClient struct {
	t        *testing.T
	writer   io.Writer
	reader   io.Reader
	sequence int
}

func (client *matrixStdioClient) tool(name string, arguments any) matrixToolResponse {
	return client.rpc("tools/call", map[string]any{"name": name, "arguments": arguments})
}
func (client *matrixStdioClient) rpc(method string, params any) matrixToolResponse {
	client.t.Helper()
	client.sequence++
	wire, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": client.sequence, "method": method, "params": params})
	require.NoError(client.t, authorizedexecute.WriteSealedFrame(client.writer, wire, authorizedexecute.DefaultLimits.FrameBytes))
	response, err := authorizedexecute.ReadBoundedFrame(client.reader, authorizedexecute.DefaultLimits.FrameBytes)
	require.NoError(client.t, err)
	if method == "initialize" {
		var envelope struct {
			Error any `json:"error"`
		}
		require.NoError(client.t, json.Unmarshal(response, &envelope), string(response))
		require.Nil(client.t, envelope.Error, string(response))
		return matrixToolResponse{}
	}
	return decodeMatrixResponse(client.t, response)
}

func decodeMatrixResponse(t *testing.T, wire []byte) matrixToolResponse {
	t.Helper()
	var envelope struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
		Error any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(wire, &envelope), string(wire))
	require.Nil(t, envelope.Error, string(wire))
	var response matrixToolResponse
	require.NoError(t, json.Unmarshal(envelope.Result.StructuredContent, &response), string(wire))
	return response
}
