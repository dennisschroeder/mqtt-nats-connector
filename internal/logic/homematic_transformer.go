package logic

import (
	"strings"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

type HomematicTransformer struct{}

func (t *HomematicTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "ccu2/")
}

func (t *HomematicTransformer) Transform(topic string, payload []byte) (string, string, *iotv1.EventEnvelope) {
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
	return "ccu2", deviceID, envelope
}