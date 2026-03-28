package logic

import (
	"strings"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/binary_sensor"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
)

type FritzTransformer struct{}

func (t *FritzTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "fritz-presence-bridge/")
}

func (t *FritzTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
	deviceID := topic // Fallback
	parts := strings.Split(topic, "/")
	if len(parts) >= 3 {
		deviceID = parts[2]
	}
	
	state := common.BinaryState_BINARY_STATE_OFF
	if string(payload) == "ON" || string(payload) == "true" {
		state = common.BinaryState_BINARY_STATE_ON
	}

	event := &envelope.EventEnvelope{
		Payload: &envelope.EventEnvelope_BinarySensor{
			BinarySensor: &binary_sensor.BinarySensorEvent{
				EntityId:    deviceID,
				State:       state,
				DeviceClass: "presence",
			},
		},
	}
	return "fritz-presence-bridge", deviceID, event
}
