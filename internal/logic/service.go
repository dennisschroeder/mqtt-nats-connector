package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/action"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-utils-go/pkg/areas"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/nats"
	natsgo "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Transformer interface {
	Accepts(topic string) bool
	Transform(topic string, payload []byte) (source string, deviceID string, envelope *envelope.EventEnvelope)
}

type MultiTransformer interface {
	TransformMulti(topic string, payload []byte) (source string, deviceID string, envelopes []*envelope.EventEnvelope)
}

type Service struct {
	mqtt         *mqtt.Client
	nats         *nats.Client
	topics       []string
	transformers []Transformer
}

func NewService(m *mqtt.Client, n *nats.Client, topics []string) *Service {
	return &Service{
		mqtt:   m,
		nats:   n,
		topics: topics,
		transformers: []Transformer{
			&Z2MTransformer{},
			&FritzTransformer{},
			&AstoTransformer{},
			&HomematicTransformer{},
			&StiebelTransformer{},
			&ZWaveTransformer{},
		},
	}
}

func (s *Service) Run(ctx context.Context) error {
	for _, topic := range s.topics {
		slog.Info("Mirroring topic", "topic", topic)
		err := s.mqtt.Subscribe(topic, func(mqttTopic string, payload []byte) {
			slog.Debug("Received MQTT message", "topic", mqttTopic, "payload", string(payload))

			var transformer Transformer
			for _, t := range s.transformers {
				if t.Accepts(mqttTopic) {
					transformer = t
					break
				}
			}

			if transformer == nil {
				slog.Debug("No transformer found for topic", "topic", mqttTopic)
				return
			}

			var source, deviceID string
			var eventEnvelopes []*envelope.EventEnvelope

			if mt, ok := transformer.(MultiTransformer); ok {
				source, deviceID, eventEnvelopes = mt.TransformMulti(mqttTopic, payload)
			} else {
				var env *envelope.EventEnvelope
				source, deviceID, env = transformer.Transform(mqttTopic, payload)
				if env != nil {
					eventEnvelopes = append(eventEnvelopes, env)
				}
			}

			if len(eventEnvelopes) == 0 {
				return
			}

			// Resolve Area via iot-utils-go (ADR 009)
			areaSlug := "global"
			if area, ok := areas.GetByEntityID(deviceID); ok {
				areaSlug = area.Slug
			}

			for _, eventEnvelope := range eventEnvelopes {
				if eventEnvelope == nil {
					continue
				}

				// Wrap metadata
				eventEnvelope.Id = fmt.Sprintf("evt_%d", time.Now().UnixNano())
				eventEnvelope.Source = source
				eventEnvelope.Topic = mqttTopic
				eventEnvelope.Timestamp = timestamppb.Now()

				// Determine Domain
				domain := "unknown"
				switch eventEnvelope.Payload.(type) {
				case *envelope.EventEnvelope_BinarySensor:
					domain = "binary_sensor"
				case *envelope.EventEnvelope_Light:
					domain = "light"
				case *envelope.EventEnvelope_Sensor:
					domain = "sensor"
				}

				// Construct NATS Subject (ADR 010)
				natsSubject := fmt.Sprintf("iot.v1.events.%s.%s.%s.%s", source, areaSlug, domain, deviceID)
				natsSubject = strings.ReplaceAll(natsSubject, "/", ".")

				data, err := proto.Marshal(eventEnvelope)
				if err != nil {
					slog.Error("Failed to marshal event", "error", err)
					continue
				}

				slog.Info("Publishing NATS event", "subject", natsSubject, "area", areaSlug)
				if err := s.nats.Publish(natsSubject, data); err != nil {
					slog.Error("Failed to publish to NATS", "subject", natsSubject, "error", err)
				}

				// Extract plain value for State Store (KV)
				if domain == "sensor" {
					if sensorEvent := eventEnvelope.GetSensor(); sensorEvent != nil {
						key := fmt.Sprintf("%s.%s", domain, sensorEvent.Id)
						if err := s.nats.PutKV(key, []byte(sensorEvent.Value)); err != nil {
							slog.Error("Failed to update KV state", "key", key, "error", err)
						} else {
							slog.Debug("Updated KV state", "key", key, "value", sensorEvent.Value)
						}
					}
				} else if domain == "binary_sensor" {
					if bsEvent := eventEnvelope.GetBinarySensor(); bsEvent != nil {
						key := fmt.Sprintf("%s.%s", domain, bsEvent.EntityId)
						val := "OFF"
						if bsEvent.State == common.BinaryState_BINARY_STATE_ON {
							val = "ON"
						}
						if err := s.nats.PutKV(key, []byte(val)); err != nil {
							slog.Error("Failed to update KV state", "key", key, "error", err)
						} else {
							slog.Debug("Updated KV state", "key", key, "value", val)
						}
					}
				} else if domain == "light" {
					if lightEvent := eventEnvelope.GetLight(); lightEvent != nil {
						key := fmt.Sprintf("%s.%s", domain, lightEvent.EntityId)
						val := "OFF"
						if lightEvent.State == common.BinaryState_BINARY_STATE_ON {
							val = "ON"
						}
						if err := s.nats.PutKV(key, []byte(val)); err != nil {
							slog.Error("Failed to update KV state", "key", key, "error", err)
						} else {
							slog.Debug("Updated KV state", "key", key, "value", val)
						}
					}
				}
			}
		})
		if err != nil {
			return err
		}
	}

	// Action Egress: NATS -> MQTT
	_, err := s.nats.Subscribe("iot.v1.actions.>", func(msg *natsgo.Msg) {
		var req action.ActionRequest
		if err := proto.Unmarshal(msg.Data, &req); err != nil {
			slog.Error("Failed to unmarshal ActionRequest", "error", err)
			return
		}
		
		slog.Info("Received NATS action", "id", req.Id, "target", req.TargetEntity)
		
		if lightCmd := req.GetLight(); lightCmd != nil {
			state := "OFF"
			if lightCmd.State == common.BinaryState_BINARY_STATE_ON {
				state = "ON"
			}
			
			payload := map[string]interface{}{
				"state": state,
			}
			if lightCmd.Brightness > 0 {
				payload["brightness"] = int(lightCmd.Brightness * 255.0)
			}
			
			data, _ := json.Marshal(payload)
			// For simplicity we route all light actions to zigbee
			topic := fmt.Sprintf("zigbee/%s/set", req.TargetEntity)
			
			slog.Info("Executing Light Action via MQTT", "topic", topic, "payload", string(data))
			if err := s.mqtt.Publish(topic, data); err != nil {
				slog.Error("Failed to publish MQTT action", "topic", topic, "error", err)
			}
		}
	})
	if err != nil {
		slog.Error("Failed to subscribe to actions on NATS", "error", err)
		return err
	}

	<-ctx.Done()
	return nil
}