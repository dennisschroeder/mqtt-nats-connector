package logic

import (
	"strconv"
	"strings"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/sensor"
)

const haStatestreamSensorPrefix = "homeassistant/sensor/"

// HAStatestreamTransformer maps Home Assistant mqtt_statestream sensor states
// (homeassistant/sensor/<object_id>/state) to sensor events, so automations can
// use HA-computed values such as sun elevation. The resulting KV key is
// sensor.<object_id>, i.e. the HA entity_id.
type HAStatestreamTransformer struct{}

func (t *HAStatestreamTransformer) Accepts(topic string) bool {
	// Has the same shape as an ESPHome state topic, so it must be checked first.
	return strings.HasPrefix(topic, haStatestreamSensorPrefix) &&
		strings.HasSuffix(topic, "/state") &&
		strings.Count(topic, "/") == 3
}

func (t *HAStatestreamTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
	objectID := strings.TrimSuffix(strings.TrimPrefix(topic, haStatestreamSensorPrefix), "/state")
	if objectID == "" {
		return "homeassistant", "", nil
	}

	value := string(payload)
	event := &sensor.SensorEvent{
		Id:       objectID,
		Source:   "homeassistant",
		EntityId: objectID,
		Value:    value,
	}
	// HA reports "unavailable"/"unknown" as state; those keep only the raw value.
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		event.NumericValue = f
	}

	return "homeassistant", objectID, &envelope.EventEnvelope{
		Payload: &envelope.EventEnvelope_Sensor{Sensor: event},
	}
}
