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
	// Try JetStream first
	_, err := c.js.Publish(subject, data)
	if err != nil {
		slog.Warn("JetStream publish failed, falling back to core NATS", "subject", subject, "error", err)
		return c.nc.Publish(subject, data)
	}
	return nil
}

func (c *Client) Close() {
	c.nc.Close()
}
