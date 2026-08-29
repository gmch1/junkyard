package cloud

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/bridge"
	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/command"
)

const operationTimeout = 15 * time.Second

const (
	initialSubscriptionTimeout = 30 * time.Second
	subscriptionRetryDelay     = 5 * time.Second
	maxPayloadBytes            = 64
)

type Client struct {
	client       mqtt.Client
	topic        string
	messages     chan bridge.Message
	logger       *slog.Logger
	opTimeout    time.Duration
	retryDelay   time.Duration
	initialReady chan struct{}
	readyOnce    sync.Once
	subscribeMu  sync.Mutex
	enqueueMu    sync.Mutex
	closed       chan struct{}
	closeOnce    sync.Once
}

func NewClient(brokerURL, topic, uid string, logger *slog.Logger) (*Client, error) {
	parsed, err := url.Parse(brokerURL)
	if err != nil {
		return nil, err
	}

	c := &Client{
		topic:        topic,
		messages:     make(chan bridge.Message, 1),
		logger:       logger,
		opTimeout:    operationTimeout,
		retryDelay:   subscriptionRetryDelay,
		initialReady: make(chan struct{}),
		closed:       make(chan struct{}),
	}

	options := mqtt.NewClientOptions()
	options.AddBroker(brokerURL)
	options.SetClientID(uid)
	options.SetProtocolVersion(4)
	options.SetCleanSession(true)
	options.SetAutoReconnect(true)
	options.SetConnectRetry(false)
	options.SetKeepAlive(45 * time.Second)
	options.SetPingTimeout(10 * time.Second)
	options.SetConnectTimeout(10 * time.Second)
	options.SetOrderMatters(true)
	options.SetTLSConfig(&tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: parsed.Hostname(),
	})
	options.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		logger.Warn("Bemfa MQTT connection lost", "error", err)
	})
	options.SetReconnectingHandler(func(_ mqtt.Client, _ *mqtt.ClientOptions) {
		logger.Info("reconnecting to Bemfa MQTT")
	})
	options.SetOnConnectHandler(c.onConnect)

	c.client = mqtt.NewClient(options)
	return c, nil
}

func (c *Client) Connect(ctx context.Context) error {
	token := c.client.Connect()
	if err := waitToken(ctx, token, operationTimeout); err != nil {
		return fmt.Errorf("connect to Bemfa MQTT: %w", err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.initialReady:
		c.logger.Info("subscribed to Bemfa lamp topic", "topic", c.topic)
		return nil
	case <-time.After(initialSubscriptionTimeout):
		return errors.New("timed out subscribing to Bemfa lamp topic")
	}
}

func (c *Client) Messages() <-chan bridge.Message {
	return c.messages
}

func (c *Client) Close() {
	c.closeOnce.Do(func() { close(c.closed) })
	if c.client.IsConnected() {
		c.client.Disconnect(250)
	}
}

func (c *Client) onConnect(client mqtt.Client) {
	go c.ensureSubscription(client)
}

type subscriptionClient interface {
	IsConnected() bool
	Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token
}

func (c *Client) ensureSubscription(client subscriptionClient) {
	c.subscribeMu.Lock()
	defer c.subscribeMu.Unlock()

	for client.IsConnected() {
		if err := c.subscribe(client); err == nil {
			c.readyOnce.Do(func() { close(c.initialReady) })
			return
		} else {
			select {
			case <-c.closed:
				return
			default:
			}
			c.logger.Warn("Bemfa MQTT subscription failed; retrying", "error", err)
		}

		timer := time.NewTimer(c.retryDelay)
		select {
		case <-c.closed:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Client) subscribe(client subscriptionClient) error {
	token := client.Subscribe(c.topic, 1, func(_ mqtt.Client, message mqtt.Message) {
		payload := message.Payload()
		if message.Retained() {
			c.logger.Info("ignored retained Bemfa command")
			return
		}
		if message.Duplicate() {
			c.logger.Info("ignored MQTT duplicate Bemfa command")
			return
		}
		if len(payload) == 0 || len(payload) > maxPayloadBytes {
			c.logger.Info("ignored invalid-size Bemfa command", "bytes", len(payload))
			return
		}
		if _, err := command.Parse(payload); err != nil {
			c.logger.Info("ignored unsupported Bemfa command")
			return
		}
		incoming := bridge.Message{
			Payload:   append([]byte(nil), payload...),
			Retained:  false,
			Duplicate: false,
		}
		if c.enqueueLatest(incoming) {
			c.logger.Warn("replaced queued Bemfa command with the latest command")
		}
	})
	if err := waitSubscriptionToken(token, c.opTimeout, c.closed); err != nil {
		return fmt.Errorf("subscribe to Bemfa lamp topic: %w", err)
	}
	resultToken, ok := token.(interface{ Result() map[string]byte })
	if !ok {
		return errors.New("Bemfa MQTT subscribe token did not include a SUBACK result")
	}
	return validateSubscribeResult(c.topic, resultToken.Result())
}

func waitSubscriptionToken(token mqtt.Token, timeout time.Duration, closed <-chan struct{}) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-closed:
		return errors.New("MQTT client is closing")
	case <-timer.C:
		return errors.New("MQTT operation timed out")
	case <-token.Done():
		return token.Error()
	}
}

func validateSubscribeResult(topic string, result map[string]byte) error {
	grantedQoS, ok := result[topic]
	if !ok {
		return errors.New("Bemfa MQTT SUBACK did not include the requested topic")
	}
	if grantedQoS > 1 {
		return fmt.Errorf("Bemfa MQTT subscription was rejected (return code 0x%02x)", grantedQoS)
	}
	return nil
}

func (c *Client) enqueueLatest(incoming bridge.Message) bool {
	c.enqueueMu.Lock()
	defer c.enqueueMu.Unlock()

	select {
	case c.messages <- incoming:
		return false
	default:
	}

	select {
	case <-c.messages:
	default:
	}
	select {
	case c.messages <- incoming:
	default:
	}
	return true
}

func waitToken(ctx context.Context, token mqtt.Token, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("MQTT operation timed out")
	case <-token.Done():
		return token.Error()
	}
}
