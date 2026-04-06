package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
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
	sourceCache  map[string]string // deviceID -> source (e.g. "zigbee", "zwave")
	z2mScenes    map[string]map[string]int // TargetEntity -> SceneName -> SceneID
}

func NewService(m *mqtt.Client, n *nats.Client, topics []string) *Service {
	return &Service{
		mqtt:        m,
		nats:        n,
		topics:      topics,
		sourceCache: make(map[string]string),
		z2mScenes:   make(map[string]map[string]int),
		transformers: []Transformer{
			&Z2MTransformer{},
			&FritzTransformer{},
			&AstoTransformer{},
			&HomematicTransformer{},
			&StiebelTransformer{},
			&ZWaveTransformer{},
			&ESPHomeTransformer{},
		},
	}
}

func (s *Service) Run(ctx context.Context) error {
	for _, topic := range s.topics {
		slog.Info("Mirroring topic", "topic", topic)
		err := s.mqtt.Subscribe(topic, func(mqttTopic string, payload []byte) {
			slog.Debug("Received MQTT message", "topic", mqttTopic, "payload", string(payload))

			if mqttTopic == "zigbee/bridge/groups" {
				var groups []struct {
					FriendlyName string `json:"friendly_name"`
					Scenes       []struct {
						ID   int    `json:"id"`
						Name string `json:"name"`
					} `json:"scenes"`
				}
				if err := json.Unmarshal(payload, &groups); err == nil {
					for _, g := range groups {
						if _, ok := s.z2mScenes[g.FriendlyName]; !ok {
							s.z2mScenes[g.FriendlyName] = make(map[string]int)
						}
						for _, sc := range g.Scenes {
							s.z2mScenes[g.FriendlyName][sc.Name] = sc.ID
						}
					}
					slog.Info("Updated Z2M scene mappings", "groups_count", len(groups))
				}
			}

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

			// Update source cache
			s.sourceCache[deviceID] = source

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
		slog.Debug("Received message on NATS action topic", "subject", msg.Subject)
		var req action.ActionRequest
		if err := proto.Unmarshal(msg.Data, &req); err != nil {
			slog.Error("Failed to unmarshal ActionRequest", "error", err)
			return
		}
		
		slog.Info("Received NATS action", "id", req.Id, "target", req.TargetEntity)
		
		// 1. Handle Scene Actions (Priority)
		if sceneCmd := req.GetScene(); sceneCmd != nil {
			// a. Home Assistant Scene Activation
			haTopic := "homeassistant/scene/activate"
			haPayload, _ := json.Marshal(map[string]interface{}{
				"scene_id": sceneCmd.SceneId,
			})
			slog.Info("Activating Home Assistant Scene", "scene", sceneCmd.SceneId)
			s.mqtt.Publish(haTopic, haPayload)

			// b. Zigbee Group Scene Recall
			if req.TargetEntity != "" {
				z2mTopic := fmt.Sprintf("zigbee/%s/set", req.TargetEntity)
				
				// Try to parse sceneId as int or lookup from cache
				sceneIDInt := -1
				if id, err := strconv.Atoi(sceneCmd.SceneId); err == nil {
					sceneIDInt = id
				} else if mapping, ok := s.z2mScenes[req.TargetEntity]; ok {
					if id, ok := mapping[sceneCmd.SceneId]; ok {
						sceneIDInt = id
					}
				}

				var z2mPayload []byte
				if sceneIDInt != -1 {
					z2mPayload, _ = json.Marshal(map[string]interface{}{
						"scene_recall": sceneIDInt,
					})
					slog.Info("Recalling Zigbee Scene by ID", "group", req.TargetEntity, "sceneName", sceneCmd.SceneId, "sceneId", sceneIDInt)
				} else {
					z2mPayload, _ = json.Marshal(map[string]interface{}{
						"scene_recall": sceneCmd.SceneId,
					})
					slog.Info("Recalling Zigbee Scene by String (Fallback)", "group", req.TargetEntity, "scene", sceneCmd.SceneId)
				}
				
				s.mqtt.Publish(z2mTopic, z2mPayload)
			}
			return
		}

		// Handle Notification Actions
		if notifCmd := req.GetNotification(); notifCmd != nil {
			slog.Info("Action: Received notification request", "target", req.TargetEntity, "title", notifCmd.Title)
			
			// HA Notify Service Payload
			haTopic := "homeassistant/notify"
			if strings.HasPrefix(req.TargetEntity, "notify.") {
				// e.g. "notify.mobile_app_dennis" -> "homeassistant/notify/mobile_app_dennis"
				haTopic = "homeassistant/" + strings.ReplaceAll(req.TargetEntity, ".", "/")
			}
			
			haPayload, _ := json.Marshal(map[string]interface{}{
				"title":   notifCmd.Title,
				"message": notifCmd.Message,
				"data":    notifCmd.Data,
			})
			
			slog.Info("Action: Publishing to HA MQTT topic", "topic", haTopic, "payload", string(haPayload))
			if err := s.mqtt.Publish(haTopic, haPayload); err != nil {
				slog.Error("Action: Failed to publish notification to MQTT", "topic", haTopic, "error", err)
			}
			return
		}

		// 4. Handle Light Actions
		if lightCmd := req.GetLight(); lightCmd != nil {
			state := "OFF"
			zwaveVal := 0
			if lightCmd.State == common.BinaryState_BINARY_STATE_ON {
				state = "ON"
				zwaveVal = 255 // Binary default
				if lightCmd.Brightness > 0 {
					zwaveVal = int(lightCmd.Brightness * 99.0) // Multilevel default
				} else {
					zwaveVal = 99
				}
			}
			
			// Zigbee Payload
			payload := map[string]interface{}{
				"state": state,
			}
			if lightCmd.Brightness > 0 {
				payload["brightness"] = int(lightCmd.Brightness * 255.0)
			}
			
			data, _ := json.Marshal(payload)
			
			// Z-Wave Payload (TargetValue wrapper)
			zwavePayload, _ := json.Marshal(map[string]interface{}{"value": zwaveVal})
			
			// Resolve Source (Cache with Broadcast Fallback)
			sources := []string{"zigbee", "zwave", "ccu2"} // List of known sources
			if cachedSource, ok := s.sourceCache[req.TargetEntity]; ok {
				sources = []string{cachedSource}
				slog.Debug("Source cache hit", "device", req.TargetEntity, "source", cachedSource)
			} else {
				slog.Info("Source cache miss, broadcasting action to all sources", "device", req.TargetEntity)
			}

			for _, src := range sources {
				topic := fmt.Sprintf("%s/%s/set", src, req.TargetEntity)
				var finalPayload []byte
				if src == "zigbee" || src == "ccu2" {
					finalPayload = data
				} else {
					finalPayload = zwavePayload
				}
				
				// Handle Z-Wave specific topic pattern if using Z-Wave JS UI "ValueID" style
				if src == "zwave" {
					// 1. Try Multilevel (Endpoint 1 is standard for many Z-Wave switches)
					mlTopic := fmt.Sprintf("zwave/%s/switch_multilevel/endpoint_1/targetValue/set", req.TargetEntity)
					slog.Info("Executing Z-Wave Multilevel Action", "topic", mlTopic, "payload", string(zwavePayload))
					s.mqtt.Publish(mlTopic, []byte(zwavePayload))
					
					// Use the simple /set topic as general fallback for Z-Wave
					finalPayload = []byte(zwavePayload)
				}

				slog.Info("Executing Light Action via MQTT", "topic", topic, "source", src)
				if err := s.mqtt.Publish(topic, finalPayload); err != nil {
					slog.Error("Failed to publish MQTT action", "topic", topic, "error", err)
				}
			}
		}

		// 5. Handle Cover Actions
		if coverCmd := req.GetCover(); coverCmd != nil {
			slog.Info("Action: Received cover request", "target", req.TargetEntity, "state", coverCmd.State)
			
			state := "STOP"
			if coverCmd.State == "OPEN" {
				state = "OPEN"
			} else if coverCmd.State == "CLOSE" {
				state = "CLOSE"
			}
			
			payload := map[string]interface{}{
				"state": state,
			}
			if coverCmd.Position != nil {
				payload["position"] = *coverCmd.Position
			}
			
			data, _ := json.Marshal(payload)
			
			// Resolve Source
			sources := []string{"zigbee", "zwave", "ccu2"}
			if cachedSource, ok := s.sourceCache[req.TargetEntity]; ok {
				sources = []string{cachedSource}
			}

			for _, src := range sources {
				var topic string
				if src == "ccu2" {
					topic = fmt.Sprintf("%s/cover/%s/set", src, req.TargetEntity)
				} else {
					topic = fmt.Sprintf("%s/%s/set", src, req.TargetEntity)
				}
				
				var finalPayload []byte
				if src == "ccu2" {
					if coverCmd.Position != nil {
						finalPayload = []byte(fmt.Sprintf("%d", *coverCmd.Position))
					} else {
						finalPayload = []byte(state)
					}
				} else {
					finalPayload = data
				}

				slog.Info("Executing Cover Action via MQTT", "topic", topic, "source", src, "payload", string(finalPayload))
				if err := s.mqtt.Publish(topic, finalPayload); err != nil {
					slog.Error("Failed to publish MQTT action for cover", "topic", topic, "error", err)
				}
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