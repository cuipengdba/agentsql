package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

var (
	errEndpointBlocked = errors.New("notification endpoint is blocked by SSRF policy")
	alwaysBlocked      = prefixes("0.0.0.0/8", "224.0.0.0/4", "240.0.0.0/4", "::/128", "ff00::/8")
	privateOrReserved  = prefixes(
		"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.31.196.0/24",
		"192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16", "192.175.48.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "2002::/16", "2620:4f:8000::/48", "3fff::/20", "5f00::/16",
		"fc00::/7", "fe80::/10",
	)
)

type safeDialer struct {
	allowPrivate bool
	dialer       net.Dialer
	resolver     *net.Resolver
}

func newSafeDialer(allowPrivate bool, timeout time.Duration) *safeDialer {
	dialer := net.Dialer{Timeout: timeout}
	return &safeDialer{allowPrivate: allowPrivate, dialer: dialer, resolver: net.DefaultResolver}
}

func (dialer *safeDialer) validateHost(ctx context.Context, host string) error {
	_, err := dialer.resolve(ctx, host)
	return err
}

func (dialer *safeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split notification endpoint: %w", err)
	}
	addresses, err := dialer.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, ip := range addresses {
		connection, dialErr := dialer.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr != nil {
			failures = append(failures, dialErr)
			continue
		}
		remoteIP, remoteErr := remoteAddressIP(connection.RemoteAddr())
		if remoteErr != nil || blockedIP(remoteIP, dialer.allowPrivate) {
			_ = connection.Close()
			if remoteErr != nil {
				failures = append(failures, remoteErr)
			} else {
				failures = append(failures, errEndpointBlocked)
			}
			continue
		}
		return connection, nil
	}
	if len(failures) == 0 {
		return nil, errors.New("notification endpoint has no usable addresses")
	}
	return nil, fmt.Errorf("dial notification endpoint: %w", errors.Join(failures...))
}

func (dialer *safeDialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" || strings.Contains(host, "%") {
		return nil, errors.New("notification endpoint host is invalid")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if blockedIP(literal, dialer.allowPrivate) {
			return nil, fmt.Errorf("%w: address class", errEndpointBlocked)
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := dialer.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve notification endpoint: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("notification endpoint resolved to no addresses")
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if blockedIP(address, dialer.allowPrivate) {
			return nil, fmt.Errorf("%w: resolved address class", errEndpointBlocked)
		}
		result = append(result, address)
	}
	return result, nil
}

func blockedIP(address netip.Addr, allowPrivate bool) bool {
	if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	for _, prefix := range alwaysBlocked {
		if prefix.Contains(address) {
			return true
		}
	}
	if allowPrivate {
		return false
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range privateOrReserved {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func remoteAddressIP(address net.Addr) (netip.Addr, error) {
	if address == nil {
		return netip.Addr{}, errors.New("notification endpoint has no remote address")
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse notification remote address: %w", err)
	}
	parsed, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse notification remote IP: %w", err)
	}
	return parsed.Unmap(), nil
}

func prefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}
