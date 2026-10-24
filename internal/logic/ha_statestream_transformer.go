package logic

import (
	"math"
	"strconv"
	"strings"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/sensor"
)

const (
	haStatestreamSource       = "homeassistant"
	haStatestreamBaseTopic    = haStatestreamSource + "/"
	haStatestreamSensorPrefix = haStatestreamBaseTopic + "sensor/"
)

// Statestream publishes unit_of_measurement only as a separate attribute
// topic, so units of mirrored entities are pinned here.
var haStatestreamUnits = map[string]string{
	"sun_solar_elevation": "°",
}

// HAStatestreamTransformer maps Home Assistant mqtt_statestream sensor states
// (homeassistant/sensor/<object_id>/state) to sensor events, so automations can
// use HA-computed values such as sun elevation. The resulting KV key is
// sensor.<object_id>, i.e. the HA entity_id.
//
// Only mirror entities that no other transformer feeds: HA's discovery mirrors
// of Z2M/ESPHome devices share their KV keys (e.g. sensor.<device>_illuminance)
// and would overwrite them in a different format.
type HAStatestreamTransformer struct{}

func (t *HAStatestreamTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, haStatestreamSensorPrefix) &&
		strings.HasSuffix(topic, "/state") &&
		strings.Count(topic, "/") == 3
}

func (t *HAStatestreamTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
	objectID := strings.TrimSuffix(strings.TrimPrefix(topic, haStatestreamSensorPrefix), "/state")
	value := string(payload)
	f, err := strconv.ParseFloat(value, 64)
	// HA reports "unknown"/"unavailable" while starting or when the source
	// drops, and statestream retains it. Dropping it keeps the last valid value
	// in the KV; consumers would otherwise read it as 0 or fail every condition.
	if objectID == "" || err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return haStatestreamSource, objectID, nil
	}

	return haStatestreamSource, objectID, &envelope.EventEnvelope{
		Payload: &envelope.EventEnvelope_Sensor{Sensor: &sensor.SensorEvent{
			Id:           objectID,
			Source:       haStatestreamSource,
			EntityId:     objectID,
			Value:        value,
			NumericValue: f,
			Unit:         haStatestreamUnits[objectID],
		}},
	}
}
