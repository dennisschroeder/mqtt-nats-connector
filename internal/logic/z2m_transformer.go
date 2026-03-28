package logic

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/binary_sensor"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/light"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/sensor"
)

type Z2MColor struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
}

type Z2MPayload struct {
	Occupancy   *bool     `json:"occupancy"`
	State       *string   `json:"state"`
	Brightness  *float32  `json:"brightness"`
	ColorTemp   *int32    `json:"color_temp"`
	ColorMode   *string   `json:"color_mode"`
	Color       *Z2MColor `json:"color,omitempty"`
	Illuminance *float64  `json:"illuminance"`
	Temperature *float64  `json:"temperature"`
}

type Z2MTransformer struct{}

func (t *Z2MTransformer) Accepts(topic string) bool {
	return strings.HasPrefix(topic, "zigbee/")
}

func (t *Z2MTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
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

	event := &envelope.EventEnvelope{}

	// Detection logic: PIR vs Light vs Raw Fallback
	if strings.Contains(deviceID, "motion") || strings.Contains(deviceID, "presence") || data.Occupancy != nil {
		state := common.BinaryState_BINARY_STATE_OFF
		if data.Occupancy != nil && *data.Occupancy {
			state = common.BinaryState_BINARY_STATE_ON
		}
		deviceClass := "motion"
		if strings.Contains(deviceID, "presence") {
			deviceClass = "presence"
		}

		event.Payload = &envelope.EventEnvelope_BinarySensor{
			BinarySensor: &binary_sensor.BinarySensorEvent{
				EntityId:    deviceID,
				State:       state,
				DeviceClass: deviceClass,
			},
		}
	} else if data.State != nil || data.Brightness != nil {
		state := common.BinaryState_BINARY_STATE_OFF
		if data.State != nil && strings.ToUpper(*data.State) == "ON" {
			state = common.BinaryState_BINARY_STATE_ON
		}

		var brightness float32
		if data.Brightness != nil {
			brightness = *data.Brightness / 255.0
		}

		lightEvt := &light.LightEvent{
			EntityId:   deviceID,
			State:      state,
			Brightness: brightness,
		}

		if data.ColorTemp != nil {
			lightEvt.ColorTemp = data.ColorTemp
		}
		if data.ColorMode != nil {
			lightEvt.ColorMode = data.ColorMode
		}
		if data.Color != nil {
			lightEvt.Color = &common.ColorXY{
				X: data.Color.X,
				Y: data.Color.Y,
			}
		}

		event.Payload = &envelope.EventEnvelope_Light{
			Light: lightEvt,
		}
	} else if data.Illuminance != nil {
		// Map Illuminance to a generic SensorEvent
		event.Payload = &envelope.EventEnvelope_Sensor{
			Sensor: &sensor.SensorEvent{
				Id:           deviceID + "_illuminance",
				Source:       "zigbee",
				EntityId:     deviceID,
				Value:        fmt.Sprintf("%f", *data.Illuminance),
				NumericValue: *data.Illuminance,
				Unit:         "lx",
			},
		}
	} else if data.Temperature != nil {
		// Map Temperature to a generic SensorEvent
		event.Payload = &envelope.EventEnvelope_Sensor{
			Sensor: &sensor.SensorEvent{
				Id:           deviceID + "_temperature",
				Source:       "zigbee",
				EntityId:     deviceID,
				Value:        fmt.Sprintf("%f", *data.Temperature),
				NumericValue: *data.Temperature,
				Unit:         "°C",
			},
		}
	} else {
		// Fallback for discovery mode
		slog.Info("DISCOVERY MODE: Unmapped Z2M payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return "zigbee", deviceID, nil
	}

	return "zigbee", deviceID, event
}