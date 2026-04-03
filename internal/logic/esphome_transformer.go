package logic

import (
	"strconv"
	"strings"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/binary_sensor"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/sensor"
)

type ESPHomeTransformer struct{}

func (t *ESPHomeTransformer) Accepts(topic string) bool {
	// Matches ESPHome default state topics like: <device_name>/<domain>/<component>/state
	return strings.Count(topic, "/") == 3 && strings.HasSuffix(topic, "/state")
}

func (t *ESPHomeTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 {
		return "esphome", "", nil
	}

	deviceID := parts[0]
	domain := parts[1]
	component := parts[2]
	payloadStr := string(payload)

	var event *envelope.EventEnvelope

	if domain == "binary_sensor" {
		state := common.BinaryState_BINARY_STATE_OFF
		if payloadStr == "ON" || payloadStr == "true" {
			state = common.BinaryState_BINARY_STATE_ON
		}

		// Map occupancy/presence to standard deviceID entity to match Z2M behavior
		entityID := deviceID
		if component != "occupancy" && component != "presence" {
			entityID = deviceID + "_" + component
		}

		deviceClass := component

		event = &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_BinarySensor{
				BinarySensor: &binary_sensor.BinarySensorEvent{
					EntityId:    entityID,
					State:       state,
					DeviceClass: deviceClass,
				},
			},
		}
	} else if domain == "sensor" {
		valFloat, _ := strconv.ParseFloat(payloadStr, 64)
		
		unit := ""
		if component == "illuminance" {
			unit = "lx"
		} else if component == "temperature" {
			unit = "°C"
		}

		event = &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_Sensor{
				Sensor: &sensor.SensorEvent{
					Id:           deviceID + "_" + component,
					Source:       "esphome",
					EntityId:     deviceID,
					Value:        payloadStr,
					NumericValue: valFloat,
					Unit:         unit,
				},
			},
		}
	} else {
		return "esphome", deviceID, nil
	}

	return "esphome", deviceID, event
}
