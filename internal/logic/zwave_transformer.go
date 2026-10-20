package logic

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/light"
)

type ZWaveTransformer struct{}

func (t *ZWaveTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "zwave/")
}

func (t *ZWaveTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
	trimmed := strings.TrimPrefix(topic, "zwave/")
	parts := strings.Split(trimmed, "/")
	deviceID := parts[0]
	
	if len(parts) < 3 {
		return "zwave", deviceID, nil
	}

	commandClass := parts[1] // switch_multilevel, manufacturer_specific, etc.
	// Filter for relevant command classes
	if commandClass != "switch_multilevel" && commandClass != "switch_binary" {
		slog.Debug("DISCOVERY MODE: Unmapped Z-Wave payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return "zwave", deviceID, nil
	}

	// All endpoints share one entity (and KV key), and actions target
	// endpoint_1. On the Fibaro FGD-212 dimmers endpoint_2 is the S2 input
	// channel, not the load, so mixing it in flips the light state.
	if endpointIndex(parts) >= 2 {
		slog.Debug("Ignoring secondary Z-Wave endpoint", "topic", topic)
		return "zwave", deviceID, nil
	}

	lastPart := parts[len(parts)-1]
	event := &envelope.EventEnvelope{}

	if lastPart == "On" {
		event.Payload = &envelope.EventEnvelope_Light{
			Light: &light.LightEvent{
				EntityId: deviceID,
				State:    common.BinaryState_BINARY_STATE_ON,
			},
		}
	} else if lastPart == "Off" {
		event.Payload = &envelope.EventEnvelope_Light{
			Light: &light.LightEvent{
				EntityId: deviceID,
				State:    common.BinaryState_BINARY_STATE_OFF,
			},
		}
	} else if lastPart == "currentValue" {
		var data map[string]interface{}
		if err := json.Unmarshal(payload, &data); err == nil {
			if val, ok := data["value"]; ok {
				if num, ok := val.(float64); ok {
					state := common.BinaryState_BINARY_STATE_OFF
					if num > 0 {
						state = common.BinaryState_BINARY_STATE_ON
					}
					event.Payload = &envelope.EventEnvelope_Light{
						Light: &light.LightEvent{
							EntityId:   deviceID,
							State:      state,
							Brightness: float32(num) / 99.0, // Z-Wave brightness is typically 0-99
						},
					}
				}
			}
		}
	} else {
		slog.Debug("DISCOVERY MODE: Unmapped Z-Wave payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return "zwave", deviceID, nil
	}

	if event.Payload == nil {
		return "zwave", deviceID, nil
	}

	return "zwave", deviceID, event
}

// endpointIndex returns N for an "endpoint_N" topic segment, or 0 if there is
// none (zwave-js-ui omits it for the root device in some topic layouts).
func endpointIndex(parts []string) int {
	for _, p := range parts {
		if n, ok := strings.CutPrefix(p, "endpoint_"); ok {
			if i, err := strconv.Atoi(n); err == nil {
				return i
			}
		}
	}
	return 0
}
