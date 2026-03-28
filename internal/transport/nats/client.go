package nats

import (
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"
)

type Client struct {
	nc *nats.Conn
	js nats.JetStreamContext
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
	slog.Info("NATS JetStream client connected", "url", url)
	return &Client{nc: nc, js: js}, nil
}

func (c *Client) Publish(subject string, data []byte) error {
	// Use core NATS publish to avoid 5s JetStream timeout if no stream exists
	return c.nc.Publish(subject, data)
}

func (c *Client) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, handler)
}

func (c *Client) Close() {
	c.nc.Close()
}
