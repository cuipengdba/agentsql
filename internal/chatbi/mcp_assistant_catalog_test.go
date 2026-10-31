package chatbi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAssistantCatalogTLSLeafFingerprintCheck(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("synthetic-leaf-certificate")}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	actual := sha256.Sum256(cert.Raw)
	if err := verifyAssistantCatalogTLSPin(state, hex.EncodeToString(actual[:])); err != nil {
		t.Fatalf("matching leaf fingerprint rejected: %v", err)
	}
	wrong := sha256.Sum256([]byte("another-leaf"))
	if err := verifyAssistantCatalogTLSPin(state, hex.EncodeToString(wrong[:])); err == nil {
		t.Fatal("mismatched leaf fingerprint accepted")
	}
	if err := verifyAssistantCatalogTLSPin(tls.ConnectionState{}, hex.EncodeToString(actual[:])); err == nil {
		t.Fatal("missing peer certificate accepted")
	}
}

func TestAssistantCatalogHTTPSRequiresTrustedChainAndRegisteredLeaf(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/sources" || r.Header.Get("X-Catalog-Token") != "synthetic-token" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders","password":"private-password"}]}`))
	}))
	defer server.Close()
	cert := server.Certificate()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	digest := sha256.Sum256(cert.Raw)
	auth := `[{"key":"X-Catalog-Token","value":"synthetic-token","target":"header"}]`
	bridge := AssistantCatalogBridge{Endpoint: server.URL + "/sources", Authorization: auth,
		Workspace: principal.Workspace, User: principal.User.ID,
		Sources:      []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, TLSCertSHA256: hex.EncodeToString(digest[:])}
	ctx := context.Background()
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	_, entries, err := service.fetchAssistantCatalogWithRoots(ctx, bridge.Endpoint, auth, nil, roots)
	if err != nil || len(entries) != 1 || entries[0].CatalogID != "81" || calls.Load() != 1 || gateway.queries != 0 {
		t.Fatalf("trusted pinned TLS catalog failed: %v, entries=%v calls=%d", err, entries, calls.Load())
	}
	if _, _, err := service.fetchAssistantCatalog(ctx, bridge.Endpoint, auth, nil); err == nil || calls.Load() != 1 {
		t.Fatal("private test CA was trusted without a configured root")
	}
	wrong := sha256.Sum256([]byte("another-certificate"))
	bridge.TLSCertSHA256 = hex.EncodeToString(wrong[:])
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.fetchAssistantCatalogWithRoots(ctx, bridge.Endpoint, auth, nil, roots); err == nil || calls.Load() != 1 || gateway.queries != 0 {
		t.Fatal("wrong registered certificate reached catalog handler or query gateway")
	}
}

func TestAssistantCatalogFetchUsesPinnedIPAndDiscardsConnectionFields(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/sources" || r.URL.Query().Get("tenant") != "east" ||
			r.Header.Get("X-Catalog-Token") != "synthetic-token" || r.Header.Get("Cookie") != "sid=synthetic-cookie" {
			t.Errorf("catalog request did not preserve registered credential targets")
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders","host":"private-host","user":"private-user","password":"private-password"}]}`))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://catalog.example.test:" + parsed.Port() + "/sources"
	auth := `[{"key":"X-Catalog-Token","value":"synthetic-token","target":"header"},{"key":"sid","value":"synthetic-cookie","target":"cookie"},{"key":"tenant","value":"east","target":"param"}]`
	bridge := AssistantCatalogBridge{Endpoint: endpoint, Authorization: auth, Workspace: principal.Workspace,
		User: principal.User.ID, Sources: []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
	if err := service.Store.ConfigureConnections(context.Background(), ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "catalog.example.test" {
			return nil, errors.New("unexpected lookup")
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	caller, entries, err := service.fetchAssistantCatalog(context.Background(), endpoint, auth, lookup)
	if err != nil || caller.User.ID != principal.User.ID || len(entries) != 1 || entries[0].CatalogID != "81" ||
		entries[0].Datasource != "10" || entries[0].Name != "Orders" || calls.Load() != 1 || gateway.queries != 0 {
		t.Fatalf("registered catalog did not return mapped public metadata: %v %#v", err, entries)
	}
	raw, _ := json.Marshal(entries)
	if strings.Contains(string(raw), "private-password") || strings.Contains(string(raw), "private-host") {
		t.Fatal("catalog connection fields escaped into response metadata")
	}
	// Reject a mixed DNS answer before any connection, even if its first IP is
	// approved. The transport must not look up the hostname again after pinning.
	mixed := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("169.254.169.254")}}, nil
	}
	if _, _, err := service.fetchAssistantCatalog(context.Background(), endpoint, auth, mixed); !errors.Is(err, errAssistantCatalog) || calls.Load() != 1 {
		t.Fatalf("mixed DNS response reached catalog: %v", err)
	}
	wrongAuth := strings.Replace(auth, "synthetic-token", "different-token", 1)
	if _, _, err := service.fetchAssistantCatalog(context.Background(), endpoint, wrongAuth, lookup); !errors.Is(err, ErrUnauthenticated) || calls.Load() != 1 {
		t.Fatalf("unregistered credential reached catalog: %v", err)
	}
}

func TestAssistantCatalogFetchFailsClosedOnRedirectAndUnmappedEntry(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/sources", http.StatusFound)
			return
		}
		if r.URL.Path == "/large" {
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders"}],"padding":"` + strings.Repeat("x", 1<<20) + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"data":[{"id":99,"name":"Foreign","password":"private-password"}]}`))
	}))
	defer server.Close()
	for _, path := range []string{"/redirect", "/sources", "/large"} {
		endpoint := server.URL + path
		auth := fmt.Sprintf(`[{"key":"token","value":"synthetic-%s","target":"header"}]`, strings.TrimPrefix(path, "/"))
		bridge := AssistantCatalogBridge{Endpoint: endpoint, Authorization: auth, Workspace: principal.Workspace,
			User: principal.User.ID, Sources: []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
			AllowedCIDRs: []string{"127.0.0.1/32"}, AllowHTTP: true}
		if err := service.Store.ConfigureConnections(context.Background(), ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
			t.Fatal(err)
		}
		before := calls.Load()
		if _, _, err := service.fetchAssistantCatalog(context.Background(), endpoint, auth, nil); !errors.Is(err, errAssistantCatalog) || calls.Load() != before+1 || gateway.queries != 0 {
			t.Fatalf("redirect, unregistered entry or oversized catalog escaped validation: %s %v", path, err)
		}
	}
}

func TestAssistantCatalogFetchRechecksCallerAfterNetworkResponse(t *testing.T) {
	service, principal, gateway, _, _ := serviceFixture(t)
	ctx := context.Background()
	updateErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		user, revision, err := service.Store.User(ctx, principal.User.ID)
		if err == nil {
			user.Status = 0
			err = service.Store.put(ctx, userKey(user.ID), user, revision)
		}
		updateErr <- err
		_, _ = w.Write([]byte(`{"code":0,"data":[{"id":81,"name":"Orders"}]}`))
	}))
	defer server.Close()
	bridge := AssistantCatalogBridge{Endpoint: server.URL + "/sources", Workspace: principal.Workspace, User: principal.User.ID,
		Authorization: `[{"key":"token","value":"synthetic-token","target":"header"}]`,
		Sources:       []AssistantCatalogSource{{CatalogID: "81", Datasource: "10"}},
		AllowedCIDRs:  []string{"127.0.0.1/32"}, AllowHTTP: true}
	if err := service.Store.ConfigureConnections(ctx, ConnectionConfig{AssistantCatalogs: []AssistantCatalogBridge{bridge}}); err != nil {
		t.Fatal(err)
	}
	if _, entries, err := service.fetchAssistantCatalog(ctx, bridge.Endpoint, bridge.Authorization, nil); !errors.Is(err, ErrUnauthenticated) || len(entries) != 0 || gateway.queries != 0 {
		t.Fatalf("revoked caller received catalog metadata: %v %#v", err, entries)
	}
	if err := <-updateErr; err != nil {
		t.Fatalf("synthetic revocation failed: %v", err)
	}
}
