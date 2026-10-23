package businessdb

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godoes/gorm-dameng/dm8/security"
	"github.com/stretchr/testify/require"
)

func TestDMDriverTLSVerifiesCertificateAndName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	certificate := server.Certificate()
	serverName := ""
	if len(certificate.DNSNames) > 0 {
		serverName = certificate.DNSNames[0]
	} else {
		require.NotEmpty(t, certificate.IPAddresses)
		serverName = certificate.IPAddresses[0].String()
	}
	writeCA := func(name string, raw []byte) string {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0o600))
		return path
	}
	trustedCA := writeCA("trusted.pem", certificate.Raw)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	otherTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(9384), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	otherDER, err := x509.CreateCertificate(rand.Reader, otherTemplate, otherTemplate, &otherKey.PublicKey, otherKey)
	require.NoError(t, err)
	wrongCA := writeCA("wrong.pem", otherDER)
	connect := func(ca, name string) (*tls.Conn, error) {
		plain, err := net.Dial("tcp", server.Listener.Addr().String())
		require.NoError(t, err)
		client, err := security.NewTLSFromTCP(plain, "", "", ca, name)
		if err != nil {
			_ = plain.Close()
		}
		return client, err
	}
	client, err := connect(trustedCA, serverName)
	require.NoError(t, err)
	state := client.ConnectionState()
	require.GreaterOrEqual(t, state.Version, uint16(tls.VersionTLS12))
	require.NotEmpty(t, state.VerifiedChains)
	require.NoError(t, client.Close())

	_, err = connect(wrongCA, serverName)
	require.Error(t, err)
	_, err = connect(trustedCA, "not-the-database.example")
	require.Error(t, err)
	_, err = connect("", serverName)
	require.Error(t, err)
}
