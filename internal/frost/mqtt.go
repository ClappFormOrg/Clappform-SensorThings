package frost

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// ObservationPublisher writes Observation-create requests to a FROST
// target over MQTT — an alternative transport to the HTTP POST write
// path. Only the leaf Observation write goes over MQTT: entity upserts
// (GetOrCreate) and the idempotency probe stay on HTTP because MQTT
// publish is fire-and-forget and returns neither an @iot.id nor a
// status code.
type ObservationPublisher interface {
	// PublishObservation publishes payload to the Observations topic for
	// datastreamID. A nil return means the broker accepted the message
	// (QoS-dependent); it does not confirm FROST persistence. Errors are
	// classified transient so the scheduler retries per F4.
	PublishObservation(ctx context.Context, datastreamID int64, payload any) error
	// Close disconnects the broker connection.
	Close()
}

// MQTTConfig configures an MQTTPublisher. BrokerURL uses a scheme paho
// understands: wss:// (WebSocket over TLS, as WBD exposes), ws://,
// tcp://, or ssl://.
type MQTTConfig struct {
	BrokerURL      string
	Username       string
	Password       string
	ClientID       string
	TopicPrefix    string // STA version segment mirrored in topics; default "v1.1"
	QoS            byte   // 0/1/2; default 1 (at-least-once)
	ConnectTimeout time.Duration
	PublishTimeout time.Duration
	// InsecureSkipVerify disables TLS verification for wss:// / ssl://
	// brokers. Testbed only (see FROST_TLS_INSECURE_SKIP_VERIFY).
	InsecureSkipVerify bool
}

func (c *MQTTConfig) applyDefaults() {
	if c.TopicPrefix == "" {
		c.TopicPrefix = "v1.1"
	}
	if c.QoS == 0 {
		c.QoS = 1
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.PublishTimeout <= 0 {
		c.PublishTimeout = 15 * time.Second
	}
	if c.ClientID == "" {
		c.ClientID = "geonovum-translation-layer"
	}
}

// MQTTPublisher is a paho-backed ObservationPublisher. It is safe for
// concurrent use (paho serializes publishes internally) and reconnects
// automatically on connection loss.
type MQTTPublisher struct {
	client         mqtt.Client
	qos            byte
	topicPrefix    string
	publishTimeout time.Duration
	logger         *slog.Logger
}

var _ ObservationPublisher = (*MQTTPublisher)(nil)

// NewMQTTPublisher connects to the broker and returns a publisher. It
// does not fail startup on an initial connection miss: auto-reconnect
// keeps retrying in the background and individual publishes surface a
// transient error until the link is up. A hard configuration error
// (unusable broker URL) is returned.
func NewMQTTPublisher(cfg MQTTConfig, logger *slog.Logger) (*MQTTPublisher, error) {
	cfg.applyDefaults()
	if cfg.BrokerURL == "" {
		return nil, fmt.Errorf("frost mqtt: empty broker URL")
	}
	if logger == nil {
		logger = slog.Default()
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(cfg.BrokerURL)
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
		opts.SetPassword(cfg.Password)
	}
	opts.SetConnectTimeout(cfg.ConnectTimeout)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	if cfg.InsecureSkipVerify {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // testbed opt-in
	}
	opts.SetOnConnectHandler(func(mqtt.Client) {
		logger.Info("frost mqtt connected", slog.String("broker", cfg.BrokerURL))
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		logger.Warn("frost mqtt connection lost", slog.Any("err", err))
	})

	client := mqtt.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(cfg.ConnectTimeout) {
		// Not connected yet — proceed; ConnectRetry keeps trying.
		logger.Warn("frost mqtt initial connect pending",
			slog.String("broker", cfg.BrokerURL),
			slog.Duration("waited", cfg.ConnectTimeout),
		)
	} else if err := tok.Error(); err != nil {
		logger.Warn("frost mqtt initial connect failed; will retry",
			slog.String("broker", cfg.BrokerURL),
			slog.Any("err", err),
		)
	}

	return &MQTTPublisher{
		client:         client,
		qos:            cfg.QoS,
		topicPrefix:    cfg.TopicPrefix,
		publishTimeout: cfg.PublishTimeout,
		logger:         logger,
	}, nil
}

// topic builds the FROST Observations topic for a Datastream, mirroring
// the STA URL path: "<prefix>/Datastreams(<id>)/Observations".
func (p *MQTTPublisher) topic(datastreamID int64) string {
	return fmt.Sprintf("%s/Datastreams(%d)/Observations", p.topicPrefix, datastreamID)
}

// PublishObservation marshals payload and publishes it. Publish
// failures (not connected, timeout, broker error) are returned as
// transient so the write is retried.
func (p *MQTTPublisher) PublishObservation(ctx context.Context, datastreamID int64, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		// A payload we can't marshal will never succeed — permanent.
		return NewPermanentHTTPError(0, fmt.Errorf("frost mqtt: marshal payload: %w", err))
	}
	if !p.client.IsConnected() {
		return NewTransientHTTPError(0, fmt.Errorf("frost mqtt: broker not connected"))
	}

	topic := p.topic(datastreamID)
	tok := p.client.Publish(topic, p.qos, false, body)

	// Bound the wait by the smaller of ctx deadline and publishTimeout.
	timeout := p.publishTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	if !tok.WaitTimeout(timeout) {
		return NewTransientHTTPError(0, fmt.Errorf("frost mqtt: publish to %s timed out after %s", topic, timeout))
	}
	if err := tok.Error(); err != nil {
		return NewTransientHTTPError(0, fmt.Errorf("frost mqtt: publish to %s: %w", topic, err))
	}
	return nil
}

// Close disconnects, allowing 250ms for in-flight publishes to flush.
func (p *MQTTPublisher) Close() {
	if p.client != nil && p.client.IsConnected() {
		p.client.Disconnect(250)
	}
}
