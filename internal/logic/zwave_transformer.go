package logic

import (
	"encoding/json"
	"log/slog"
	"strings"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

type ZWaveTransformer struct{}

func (t *ZWaveTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "zwave/")
}

func (t *ZWaveTransformer) Transform(topic string, payload []byte) (string, string, *iotv1.EventEnvelope) {
	trimmed := strings.TrimPrefix(topic, "zwave/")
	parts := strings.Split(trimmed, "/")
	deviceID := parts[0]
	
	if len(parts) < 3 {
		return "zwave", deviceID, nil
	}

	commandClass := parts[1] // switch_multilevel, manufacturer_specific, etc.
	// Filter for relevant command classes
	if commandClass != "switch_multilevel" && commandClass != "switch_binary" {
		slog.Info("DISCOVERY MODE: Unmapped Z-Wave payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return "zwave", deviceID, nil
	}

	lastPart := parts[len(parts)-1]
	envelope := &iotv1.EventEnvelope{}

	if lastPart == "On" {
		envelope.Payload = &iotv1.EventEnvelope_Light{
			Light: &iotv1.LightEvent{
				EntityId: deviceID,
				State:    iotv1.BinaryState_BINARY_STATE_ON,
			},
		}
	} else if lastPart == "Off" {
		envelope.Payload = &iotv1.EventEnvelope_Light{
			Light: &iotv1.LightEvent{
				EntityId: deviceID,
				State:    iotv1.BinaryState_BINARY_STATE_OFF,
			},
		}
	} else if lastPart == "currentValue" {
		var data map[string]interface{}
		if err := json.Unmarshal(payload, &data); err == nil {
			if val, ok := data["value"]; ok {
				if num, ok := val.(float64); ok {
					state := iotv1.BinaryState_BINARY_STATE_OFF
					if num > 0 {
						state = iotv1.BinaryState_BINARY_STATE_ON
					}
					envelope.Payload = &iotv1.EventEnvelope_Light{
						Light: &iotv1.LightEvent{
							EntityId:   deviceID,
							State:      state,
							Brightness: float32(num) / 99.0, // Z-Wave brightness is typically 0-99
						},
					}
				}
			}
		}
	} else {
		slog.Info("DISCOVERY MODE: Unmapped Z-Wave payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return "zwave", deviceID, nil
	}

	if envelope.Payload == nil {
		return "zwave", deviceID, nil
	}

	return "zwave", deviceID, envelope
}