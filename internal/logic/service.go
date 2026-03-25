package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
	"github.com/dennisschroeder/iot-utils-go/pkg/areas"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/nats"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Z2MPayload struct {
	Occupancy  bool    `json:"occupancy"`
	State      string  `json:"state"`
	Brightness float32 `json:"brightness"`
}

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
			slog.Debug("Received MQTT message", "topic", mqttTopic, "payload", string(payload))

			var eventEnvelope *iotv1.EventEnvelope

			// 1. Determine Source and DeviceID
			source := "unknown"
			deviceID := mqttTopic
			eventType := "state"

			if strings.HasPrefix(mqttTopic, "zigbee/") {
				source = "zigbee"
				trimmed := strings.TrimPrefix(mqttTopic, "zigbee/")
				parts := strings.Split(trimmed, "/")
				deviceID = parts[0]
				if len(parts) > 1 {
					eventType = strings.Join(parts[1:], "/")
				}
			}

			// Ignore non-state events for Zigbee (like availability or bridge/#)
			if source == "zigbee" && (eventType != "state" || deviceID == "bridge") {
				slog.Debug("Ignoring non-state or bridge event", "topic", mqttTopic, "event_type", eventType)
				return
			}

			// 2. Resolve Area via iot-utils-go (ADR 009)
			areaSlug := "global"
			if area, ok := areas.GetByEntityID(deviceID); ok {
				areaSlug = area.Slug
			}

			// 3. Transform based on Source (ADR 008)
			if source == "zigbee" {
				eventEnvelope = s.transformZ2M(mqttTopic, deviceID, payload)
			} else {
				eventEnvelope = s.transformLegacy(mqttTopic, deviceID, payload)
			}

			if eventEnvelope == nil {
				return
			}

			// 4. Wrap metadata
			eventEnvelope.Id = fmt.Sprintf("evt_%d", time.Now().UnixNano())
			eventEnvelope.Source = source
			eventEnvelope.Topic = mqttTopic
			eventEnvelope.Timestamp = timestamppb.Now()

			// 4.5 Determine Domain
			domain := "unknown"
			switch eventEnvelope.Payload.(type) {
			case *iotv1.EventEnvelope_Presence:
				domain = "presence"
			case *iotv1.EventEnvelope_Light:
				domain = "light"
			case *iotv1.EventEnvelope_Sensor:
				domain = "sensor"
			}

			// 5. Construct NATS Subject (ADR 010)
			// Format: iot.v1.events.<source>.<area>.<domain>.<device_id>
			natsSubject := fmt.Sprintf("iot.v1.events.%s.%s.%s.%s", source, areaSlug, domain, deviceID)
			natsSubject = strings.ReplaceAll(natsSubject, "/", ".")

			data, err := proto.Marshal(eventEnvelope)
			if err != nil {
				slog.Error("Failed to marshal event", "error", err)
				return
			}

			slog.Info("Publishing NATS event", "subject", natsSubject, "area", areaSlug)
			if err := s.nats.Publish(natsSubject, data); err != nil {
				slog.Error("Failed to publish to NATS", "subject", natsSubject, "error", err)
			}
		})
		if err != nil {
			return err
		}
	}

	<-ctx.Done()
	return nil
}

func (s *Service) transformZ2M(topic, deviceID string, payload []byte) *iotv1.EventEnvelope {
	var data Z2MPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		slog.Debug("Could not parse Z2M JSON", "topic", topic, "error", err)
		return nil
	}

	envelope := &iotv1.EventEnvelope{}

	// Detection logic: PIR vs Light vs Raw Fallback
	if strings.Contains(deviceID, "motion") || strings.Contains(deviceID, "presence") || strings.Contains(string(payload), "occupancy") {
		state := iotv1.BinaryState_BINARY_STATE_OFF
		if data.Occupancy {
			state = iotv1.BinaryState_BINARY_STATE_ON
		}
		envelope.Payload = &iotv1.EventEnvelope_Presence{
			Presence: &iotv1.PresenceEvent{
				EntityId: deviceID,
				State:    state,
			},
		}
	} else if strings.Contains(string(payload), "\"state\"") || strings.Contains(string(payload), "\"brightness\"") {
		state := iotv1.BinaryState_BINARY_STATE_OFF
		if strings.ToUpper(data.State) == "ON" {
			state = iotv1.BinaryState_BINARY_STATE_ON
		}
		envelope.Payload = &iotv1.EventEnvelope_Light{
			Light: &iotv1.LightEvent{
				EntityId:   deviceID,
				State:      state,
				Brightness: data.Brightness / 255.0,
			},
		}
	} else {
		// Fallback for discovery mode
		slog.Info("DISCOVERY MODE: Unmapped Z2M payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return nil
	}

	return envelope
}

func (s *Service) transformLegacy(topic, deviceID string, payload []byte) *iotv1.EventEnvelope {
	state := iotv1.BinaryState_BINARY_STATE_OFF
	if string(payload) == "ON" || string(payload) == "true" {
		state = iotv1.BinaryState_BINARY_STATE_ON
	}

	return &iotv1.EventEnvelope{
		Payload: &iotv1.EventEnvelope_Presence{
			Presence: &iotv1.PresenceEvent{
				EntityId: deviceID,
				State:    state,
			},
		},
	}
}
