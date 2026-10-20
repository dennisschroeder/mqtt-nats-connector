package nats

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

const drainTimeout = 5 * time.Second

type Client struct {
	closed chan struct{}
	nc     *nats.Conn
	js     nats.JetStreamContext
	kv     nats.KeyValue
}

func NewClient(url string) (*Client, error) {
	closed := make(chan struct{})
	var once sync.Once
	nc, err := nats.Connect(url, nats.ClosedHandler(func(*nats.Conn) {
		once.Do(func() { close(closed) })
	}))
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	// Ensure the Key-Value store exists
	kv, err := js.KeyValue("iot_state")
	if err != nil {
		if err == nats.ErrBucketNotFound {
			kv, err = js.CreateKeyValue(&nats.KeyValueConfig{
				Bucket:      "iot_state",
				Description: "Latest state of all IoT devices",
			})
			if err != nil {
				return nil, fmt.Errorf("create kv bucket: %w", err)
			}
		} else {
			return nil, fmt.Errorf("get kv bucket: %w", err)
		}
	}

	slog.Info("NATS JetStream client connected with KV store", "url", url)
	return &Client{closed: closed, nc: nc, js: js, kv: kv}, nil
}

func (c *Client) Publish(subject string, data []byte) error {
	// Use core NATS publish to avoid 5s JetStream timeout if no stream exists
	return c.nc.Publish(subject, data)
}

func (c *Client) PutKV(key string, data []byte) error {
	_, err := c.kv.Put(key, data)
	return err
}

// GetKV returns found=false without an error if the key does not exist.
func (c *Client) GetKV(key string) (value []byte, found bool, err error) {
	entry, err := c.kv.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return entry.Value(), true, nil
}

func (c *Client) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, handler)
}

// Close drains the connection so in-flight callbacks finish and pending
// publishes are flushed before it closes.
func (c *Client) Close() {
	if err := c.nc.Drain(); err != nil {
		slog.Warn("NATS drain failed, closing", "error", err)
		c.nc.Close()
		return
	}
	select {
	case <-c.closed:
	case <-time.After(drainTimeout):
		slog.Warn("NATS drain timed out, closing")
		c.nc.Close()
	}
}
