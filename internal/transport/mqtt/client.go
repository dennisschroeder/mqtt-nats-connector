package mqtt

import (
	"log/slog"
	"sync"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type Client struct {
	client mqtt.Client

	mu            sync.Mutex
	subs          map[string]MessageHandler
	connectedOnce bool
}

// MessageHandler receives a message. retained is true only for the broker's
// retained snapshot sent in response to a SUBSCRIBE; live messages and those
// queued in the persistent session arrive with retained=false.
type MessageHandler func(topic string, payload []byte, retained bool)

func NewClient(broker, clientID string) (*Client, error) {
	c := &Client{subs: make(map[string]MessageHandler)}
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetAutoReconnect(true).
		SetCleanSession(false).
		SetResumeSubs(true).
		SetOnConnectHandler(c.resubscribe).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			slog.Warn("MQTT connection lost", "error", err)
		})

	c.client = mqtt.NewClient(opts)
	if token := c.client.Connect(); token.Wait() && token.Error() != nil {
		return nil, token.Error()
	}
	slog.Info("MQTT client connected", "broker", broker)
	return c, nil
}

// resubscribe runs on every (re)connect. The broker may have lost the
// persistent session (ResumeSubs only resends unacknowledged SUBSCRIBEs), and
// each SUBSCRIBE also makes the broker resend its retained snapshot, which the
// service uses to resync state after the gap.
func (c *Client) resubscribe(client mqtt.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The initial connect has nothing to restore; Subscribe does that.
	if !c.connectedOnce {
		c.connectedOnce = true
		return
	}
	if len(c.subs) == 0 {
		return
	}
	slog.Info("MQTT (re)connected, resubscribing", "topics", len(c.subs))
	for topic, handler := range c.subs {
		if err := subscribe(client, topic, handler); err != nil {
			slog.Error("MQTT resubscribe failed", "topic", topic, "error", err)
		}
	}
}

func (c *Client) Subscribe(topic string, handler MessageHandler) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs[topic] = handler
	return subscribe(c.client, topic, handler)
}

func subscribe(client mqtt.Client, topic string, handler MessageHandler) error {
	token := client.Subscribe(topic, 1, func(_ mqtt.Client, msg mqtt.Message) {
		handler(msg.Topic(), msg.Payload(), msg.Retained())
	})
	token.Wait()
	return token.Error()
}

func (c *Client) Publish(topic string, payload []byte) error {
	slog.Info("MQTT Publish", "topic", topic, "payload", string(payload))
	token := c.client.Publish(topic, 1, false, payload)
	token.Wait()
	return token.Error()
}

func (c *Client) Close() {
	c.client.Disconnect(250)
}
