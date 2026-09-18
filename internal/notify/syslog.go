package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type syslogSender struct {
	config  SyslogConfig
	dialer  *safeDialer
	now     func() time.Time
	timeout time.Duration
}

func newSyslogSender(ctx context.Context, config ChannelConfig, options managerOptions) (*syslogSender, error) {
	dialer := newSafeDialer(config.AllowPrivateEndpoints, options.httpTimeout)
	if err := dialer.validateHost(ctx, config.Syslog.Host); err != nil {
		return nil, err
	}
	return &syslogSender{config: *config.Syslog, dialer: dialer, now: options.now, timeout: options.httpTimeout}, nil
}

func (sender *syslogSender) Send(ctx context.Context, payload Payload) error {
	address := net.JoinHostPort(sender.config.Host, strconv.Itoa(sender.config.Port))
	connection, err := sender.dialer.DialContext(ctx, sender.config.Transport, address)
	if err != nil {
		return fmt.Errorf("send syslog: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(sender.timeout)
	_ = connection.SetWriteDeadline(deadline)
	message, err := renderSyslog(sender.config.Facility, payload, sender.now())
	if err != nil {
		return err
	}
	if sender.config.Transport == "tcp" {
		message = append(message, '\n')
	}
	if _, err := connection.Write(message); err != nil {
		return fmt.Errorf("write syslog: %w", err)
	}
	return nil
}

func renderSyslog(facility int, payload Payload, now time.Time) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode syslog payload: %w", err)
	}
	priority := facility*8 + severity(payload.Decision)
	structured := `[agentsql@32473 product="AgentSQL" audit_id="` + escapeStructured(strconv.FormatInt(payload.AuditID, 10)) + `"]`
	prefix := "<" + strconv.Itoa(priority) + ">1 " + now.UTC().Format(time.RFC3339Nano) + " - AgentSQL - audit " + structured + " "
	return append([]byte(prefix), encoded...), nil
}

func severity(decision string) int {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "deny", "error":
		return 3 // err
	case "warn":
		return 4 // warning
	case "approve":
		return 5 // notice
	default:
		return 6 // informational
	}
}

func escapeStructured(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", `"`, `\"`, "]", "\\]")
	return replacer.Replace(value)
}
