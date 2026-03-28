package nats

import (
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
)

type Client struct {
	nc *nats.Conn
	js nats.JetStreamContext
	kv nats.KeyValue
}

func NewClient(url string) (*Client, error) {
	nc, err := nats.Connect(url)
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
				Bucket: "iot_state",
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
	return &Client{nc: nc, js: js, kv: kv}, nil
}

func (c *Client) Publish(subject string, data []byte) error {
	// Use core NATS publish to avoid 5s JetStream timeout if no stream exists
	return c.nc.Publish(subject, data)
}

func (c *Client) PutKV(key string, data []byte) error {
	_, err := c.kv.Put(key, data)
	return err
}

func (c *Client) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, handler)
}

func (c *Client) Close() {
	c.nc.Close()
}
