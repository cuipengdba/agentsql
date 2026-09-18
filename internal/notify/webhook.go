package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type webhookSender struct {
	config WebhookConfig
	client *http.Client
	now    func() time.Time
}

func newWebhookSender(ctx context.Context, config ChannelConfig, options managerOptions) (*webhookSender, error) {
	parsed, err := url.Parse(config.Webhook.URL)
	if err != nil {
		return nil, fmt.Errorf("parse webhook URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("webhook scheme must be http or https")
	}
	if parsed.Scheme == "http" && !config.AllowPrivateEndpoints {
		return nil, errors.New("http webhook requires allow_private_endpoints")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("webhook URL contains an unsupported authority or fragment")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return nil, errors.New("webhook URL port is empty")
	}
	if portText := parsed.Port(); portText != "" {
		port, portErr := strconv.Atoi(portText)
		if portErr != nil || port < 1 || port > 65535 {
			return nil, errors.New("webhook URL port must be between 1 and 65535")
		}
	}
	dialer := newSafeDialer(config.AllowPrivateEndpoints, options.httpTimeout)
	if err := dialer.validateHost(ctx, parsed.Hostname()); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   options.httpTimeout,
		ResponseHeaderTimeout: options.httpTimeout,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   options.httpTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("webhook redirects are disabled")
		},
	}
	if options.httpClient != nil {
		client = options.httpClient(config.AllowPrivateEndpoints)
	}
	return &webhookSender{config: *config.Webhook, client: client, now: options.now}, nil
}

func (sender *webhookSender) Send(ctx context.Context, payload Payload) error {
	body, err := renderWebhook(sender.config.Template, payload)
	if err != nil {
		return err
	}
	target := sender.config.URL
	if sender.config.Template == WebhookDingTalk && sender.config.Secret != "" {
		target, err = dingTalkSignedURL(target, sender.config.Secret, sender.now())
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "AgentSQL-notify/1")
	if sender.config.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+sender.config.BearerToken)
	}
	for name, value := range sender.config.Headers {
		request.Header.Set(name, value)
	}
	if sender.config.Template == WebhookGeneric && sender.config.Secret != "" {
		timestamp := strconv.FormatInt(sender.now().Unix(), 10)
		request.Header.Set("X-AgentSQL-Timestamp", timestamp)
		request.Header.Set("X-AgentSQL-Signature", genericSignature(timestamp, body, sender.config.Secret))
	}
	response, err := sender.client.Do(request)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP status %d", response.StatusCode)
	}
	return nil
}

func renderWebhook(template WebhookTemplate, payload Payload) ([]byte, error) {
	text := payloadText(payload)
	var document any
	switch template {
	case WebhookGeneric:
		document = payload
	case WebhookFeishu:
		document = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
	case WebhookDingTalk:
		document = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": "AgentSQL notification", "text": strings.ReplaceAll(text, "\n", "  \n")}}
	case WebhookWeCom:
		document = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": text}}
	case WebhookSlack:
		document = map[string]string{"text": text}
	default:
		return nil, fmt.Errorf("unsupported webhook template %q", template)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode webhook payload: %w", err)
	}
	return encoded, nil
}

func genericSignature(timestamp string, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func dingTalkSignedURL(rawURL, secret string, now time.Time) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse DingTalk URL: %w", err)
	}
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + secret))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	query := parsed.Query()
	query.Set("timestamp", timestamp)
	query.Set("sign", signature)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
