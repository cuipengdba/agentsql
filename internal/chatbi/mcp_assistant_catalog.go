package chatbi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errAssistantCatalog = errors.New("registered assistant catalog unavailable")

type assistantCatalogEntry struct {
	CatalogID  ID
	Datasource ID
	Name       string
}

type assistantCatalogLookup func(context.Context, string) ([]net.IPAddr, error)

// fetchAssistantCatalog reads metadata only from an operator-registered
// destination. Database connection fields in the response are never decoded or
// forwarded. The selected IP is checked and pinned for the entire request.
func (service *Service) fetchAssistantCatalog(ctx context.Context, endpoint, authorization string, lookup assistantCatalogLookup) (Principal, []assistantCatalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, mappings, revision, err := service.resolveAssistantCatalogBridge(ctx, endpoint, authorization)
	if err != nil {
		return Principal{}, nil, ErrUnauthenticated
	}
	credentials, err := parseMCPAssistantCatalogIdentity(endpoint, authorization)
	if err != nil {
		return Principal{}, nil, ErrUnauthenticated
	}
	var policy assistantCatalogBridgeRecord
	currentRevision, err := service.Store.get(ctx, service.Store.assistantCatalogBridgeKey(endpoint, credentials), &policy)
	if err != nil || currentRevision != revision || !policy.Enabled || policy.Endpoint != endpoint ||
		!validAssistantCatalogNetworks(policy.AllowedCIDRs) || strings.HasPrefix(endpoint, "http://") && !policy.AllowHTTP {
		return Principal{}, nil, ErrUnauthenticated
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return Principal{}, nil, errAssistantCatalog
	}
	ip, err := assistantCatalogPinnedIP(ctx, u.Hostname(), policy.AllowedCIDRs, lookup)
	if err != nil {
		return Principal{}, nil, errAssistantCatalog
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Principal{}, nil, errAssistantCatalog
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Principal{}, nil, errAssistantCatalog
	}
	for _, item := range credentials {
		switch item.Target {
		case "header":
			request.Header.Set(item.Key, item.Value)
		case "cookie":
			cookie := &http.Cookie{Name: item.Key, Value: item.Value}
			if cookie.Valid() != nil {
				return Principal{}, nil, errAssistantCatalog
			}
			request.AddCookie(cookie)
		case "param":
			if query.Has(item.Key) {
				return Principal{}, nil, errAssistantCatalog
			}
			query.Set(item.Key, item.Value)
		}
	}
	request.URL.RawQuery = query.Encode()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return Principal{}, nil, errAssistantCatalog
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 65536,
		DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
			host, requestPort, err := net.SplitHostPort(address)
			if err != nil || !strings.EqualFold(host, u.Hostname()) || requestPort != port {
				return nil, errAssistantCatalog
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
		}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return Principal{}, nil, errAssistantCatalog
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Principal{}, nil, errAssistantCatalog
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return Principal{}, nil, errAssistantCatalog
	}
	var catalog struct {
		Code *int `json:"code"`
		Data []struct {
			ID   ID     `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &catalog) != nil || catalog.Code == nil || *catalog.Code != 0 && *catalog.Code != 200 || len(catalog.Data) > 32 {
		return Principal{}, nil, errAssistantCatalog
	}
	entries := make([]assistantCatalogEntry, 0, len(catalog.Data))
	seen := map[ID]bool{}
	for _, source := range catalog.Data {
		bound := mappings[source.ID]
		if !source.ID.Valid() || bound == "" || seen[source.ID] || source.Name == "" || len(source.Name) > 256 {
			return Principal{}, nil, errAssistantCatalog
		}
		seen[source.ID] = true
		entries = append(entries, assistantCatalogEntry{CatalogID: source.ID, Datasource: bound, Name: source.Name})
	}
	current, _, afterRevision, err := service.resolveAssistantCatalogBridge(ctx, endpoint, authorization)
	if err != nil || afterRevision != revision || current.User.ID != p.User.ID ||
		current.User.AuthRevision != p.User.AuthRevision || current.Workspace != p.Workspace {
		return Principal{}, nil, ErrUnauthenticated
	}
	return p, entries, nil
}

func assistantCatalogPinnedIP(ctx context.Context, host string, cidrs []string, lookup assistantCatalogLookup) (net.IP, error) {
	if !validAssistantCatalogNetworks(cidrs) {
		return nil, errAssistantCatalog
	}
	var addresses []net.IPAddr
	if literal := net.ParseIP(host); literal != nil {
		addresses = []net.IPAddr{{IP: literal}}
	} else {
		if lookup == nil {
			lookup = net.DefaultResolver.LookupIPAddr
		}
		var err error
		addresses, err = lookup(ctx, host)
		if err != nil {
			return nil, errAssistantCatalog
		}
	}
	if len(addresses) == 0 || len(addresses) > 16 {
		return nil, errAssistantCatalog
	}
	var selected net.IP
	for _, address := range addresses {
		if address.IP == nil || address.Zone != "" || address.IP.IsUnspecified() || address.IP.IsMulticast() {
			return nil, errAssistantCatalog
		}
		approved := false
		for _, cidr := range cidrs {
			_, network, _ := net.ParseCIDR(cidr)
			if network.Contains(address.IP) {
				approved = true
				break
			}
		}
		if !approved {
			return nil, errAssistantCatalog
		}
		if selected == nil {
			selected = address.IP
		}
	}
	return selected, nil
}
