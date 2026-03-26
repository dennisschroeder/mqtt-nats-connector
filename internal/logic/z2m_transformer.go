package logic

import (
	"encoding/json"
	"log/slog"
	"strings"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

type Z2MPayload struct {
	Occupancy  bool    `json:"occupancy"`
	State      string  `json:"state"`
	Brightness float32 `json:"brightness"`
}

type Z2MTransformer struct{}

func (t *Z2MTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "zigbee/")
}

func (t *Z2MTransformer) Transform(topic string, payload []byte) (string, string, *iotv1.EventEnvelope) {
	trimmed := strings.TrimPrefix(topic, "zigbee/")
	parts := strings.Split(trimmed, "/")
	deviceID := parts[0]
	eventType := "state"
	if len(parts) > 1 {
		eventType = strings.Join(parts[1:], "/")
	}

	// Ignore non-state events for Zigbee (like availability or bridge/#)
	if eventType != "state" || deviceID == "bridge" {
		slog.Debug("Ignoring non-state or bridge event", "topic", topic, "event_type", eventType)
		return "zigbee", deviceID, nil
	}

	var data Z2MPayload
	if err := json.Unmarshal(payload, &data); err != nil {
		slog.Debug("Could not parse Z2M JSON", "topic", topic, "error", err)
		return "zigbee", deviceID, nil
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
		return "zigbee", deviceID, nil
	}

	return "zigbee", deviceID, envelope
}