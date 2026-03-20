package logic

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	iotv1 "github.com/dennisschroeder/mqtt-nats-connector/gen/go/iot/v1"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/nats"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	mqtt   *mqtt.Client
	nats   *nats.Client
	topics []string
}

func NewService(m *mqtt.Client, n *nats.Client, topics []string) *Service {
	return &Service{mqtt: m, nats: n, topics: topics}
}

func (s *Service) Run(ctx context.Context) error {
	for _, topic := range s.topics {
		slog.Info("Mirroring topic", "topic", topic)
		err := s.mqtt.Subscribe(topic, func(mqttTopic string, payload []byte) {
			event := &iotv1.SensorEvent{
				Id:        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
				Source:    "mqtt-nats-connector",
				EntityId:  mqttTopic,
				Value:     string(payload),
				Timestamp: timestamppb.Now(),
			}

			data, err := proto.Marshal(event)
			if err != nil {
				slog.Error("Failed to marshal event", "error", err)
				return
			}

			subject := "mqtt." + mqttTopic
			if err := s.nats.Publish(subject, data); err != nil {
				slog.Error("Failed to publish to NATS", "subject", subject, "error", err)
			}
		})
		if err != nil {
			return err
		}
	}

	<-ctx.Done()
	return nil
}
