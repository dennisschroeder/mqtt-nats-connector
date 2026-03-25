package logic

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/nats"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	TargetPIR = "everything_presence_one_office_pir"
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
			slog.Debug("Received MQTT message", "topic", mqttTopic, "payload_len", len(payload))
			var eventEnvelope *iotv1.EventEnvelope

			// Minimal transformation logic for PIR
			// MQTT Topic pattern (e.g., zigbee2mqtt/PIR_ID or similar)
			// Assuming payload is "ON" or "OFF" for the walkthrough
			if mqttTopic == fmt.Sprintf("stat/%s/POWER", TargetPIR) || mqttTopic == TargetPIR {
				state := iotv1.BinaryState_BINARY_STATE_OFF
				if string(payload) == "ON" {
					state = iotv1.BinaryState_BINARY_STATE_ON
				}

				eventEnvelope = &iotv1.EventEnvelope{
					Id:        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
					Source:    "mqtt-nats-connector",
					Topic:     mqttTopic,
					Timestamp: timestamppb.Now(),
					Payload: &iotv1.EventEnvelope_Presence{
						Presence: &iotv1.PresenceEvent{
							EntityId: TargetPIR,
							State:    state,
						},
					},
				}
			} else {
				// Fallback to legacy SensorEvent
				eventEnvelope = &iotv1.EventEnvelope{
					Id:        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
					Source:    "mqtt-nats-connector",
					Topic:     mqttTopic,
					Timestamp: timestamppb.Now(),
					Payload: &iotv1.EventEnvelope_Light{
						Light: &iotv1.LightEvent{
							EntityId: mqttTopic,
							// Minimal mapping for walkthrough
						},
					},
				}
			}

			data, err := proto.Marshal(eventEnvelope)
			if err != nil {
				slog.Error("Failed to marshal event", "error", err)
				return
			}

			subject := "iot.events.walkthrough"
			slog.Info("Publishing NATS event", "subject", subject, "source", eventEnvelope.Source, "topic", eventEnvelope.Topic)
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
