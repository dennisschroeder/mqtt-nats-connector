package logic

import (
	"strings"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

type FritzTransformer struct{}

func (t *FritzTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "fritz-presence-bridge/")
}

func (t *FritzTransformer) Transform(topic string, payload []byte) (string, string, *iotv1.EventEnvelope) {
	deviceID := topic // Fallback
	parts := strings.Split(topic, "/")
	if len(parts) >= 3 {
		deviceID = parts[2]
	}
	
	state := iotv1.BinaryState_BINARY_STATE_OFF
	if string(payload) == "ON" || string(payload) == "true" {
		state = iotv1.BinaryState_BINARY_STATE_ON
	}

	envelope := &iotv1.EventEnvelope{
		Payload: &iotv1.EventEnvelope_Presence{
			Presence: &iotv1.PresenceEvent{
				EntityId: deviceID,
				State:    state,
			},
		},
	}
	return "fritz-presence-bridge", deviceID, envelope
}