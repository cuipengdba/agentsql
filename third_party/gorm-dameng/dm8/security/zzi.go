/*
 * Copyright (c) 2000-2018, 达梦数据库有限公司.
 * All rights reserved.
 */

package security

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
)

// NewTLSFromTCP performs an authenticated TLS handshake. A certificate pair
// is optional for DM's one-way authentication mode; if supplied, both paths
// must be present. There is deliberately no insecure fallback.
func NewTLSFromTCP(conn net.Conn, sslCertPath, sslKeyPath, sslCAPath, serverName string) (*tls.Conn, error) {
	if sslCAPath == "" || serverName == "" {
		return nil, errors.New("verified DM TLS requires CA file and server name")
	}
	if (sslCertPath == "") != (sslKeyPath == "") {
		return nil, errors.New("DM TLS client certificate and key must be supplied together")
	}
	pem, err := os.ReadFile(sslCAPath)
	if err != nil {
		return nil, fmt.Errorf("read DM TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("DM TLS CA file contains no certificates")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName}
	if sslCertPath != "" {
		pair, err := tls.LoadX509KeyPair(sslCertPath, sslKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load DM TLS client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	tlsConn := tls.Client(conn, config)
	if err := tlsConn.Handshake(); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return tlsConn, nil
}
