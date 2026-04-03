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
	Action      *string   `json:"action"`
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

// Single Transform interface implementation (fallback if Multi is not used, though it is)
func (t *Z2MTransformer) Transform(topic string, payload []byte) (string, string, *envelope.EventEnvelope) {
	source, deviceID, envs := t.TransformMulti(topic, payload)
	if len(envs) > 0 {
		return source, deviceID, envs[0]
	}
	return source, deviceID, nil
}

// TransformMulti extracts multiple events from a single JSON payload
func (t *Z2MTransformer) TransformMulti(topic string, payload []byte) (string, string, []*envelope.EventEnvelope) {
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

	var envelopes []*envelope.EventEnvelope

	// Detection logic: PIR
	if strings.Contains(deviceID, "motion") || strings.Contains(deviceID, "presence") || data.Occupancy != nil {
		state := common.BinaryState_BINARY_STATE_OFF
		if data.Occupancy != nil && *data.Occupancy {
			state = common.BinaryState_BINARY_STATE_ON
		}
		deviceClass := "motion"
		if strings.Contains(deviceID, "presence") {
			deviceClass = "presence"
		}

		envelopes = append(envelopes, &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_BinarySensor{
				BinarySensor: &binary_sensor.BinarySensorEvent{
					EntityId:    deviceID,
					State:       state,
					DeviceClass: deviceClass,
				},
			},
		})
	}
	
	// Detection logic: Doorbell/Action
	if data.Action != nil || strings.Contains(deviceID, "doorbell") {
		state := common.BinaryState_BINARY_STATE_OFF
		if data.Action != nil && *data.Action == "pressed" {
			state = common.BinaryState_BINARY_STATE_ON
		}
		
		slog.Info("Doorbell detection", "deviceID", deviceID, "action", data.Action, "state", state)

		envelopes = append(envelopes, &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_BinarySensor{
				BinarySensor: &binary_sensor.BinarySensorEvent{
					EntityId:    deviceID,
					State:       state,
					DeviceClass: "doorbell",
				},
			},
		})
	}

	// Detection logic: Light
	if data.State != nil || data.Brightness != nil {
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

		envelopes = append(envelopes, &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_Light{
				Light: lightEvt,
			},
		})
	}

	// Detection logic: Illuminance Sensor
	if data.Illuminance != nil {
		envelopes = append(envelopes, &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_Sensor{
				Sensor: &sensor.SensorEvent{
					Id:           deviceID + "_illuminance",
					Source:       "zigbee",
					EntityId:     deviceID,
					Value:        fmt.Sprintf("%f", *data.Illuminance),
					NumericValue: *data.Illuminance,
					Unit:         "lx",
				},
			},
		})
	}

	// Detection logic: Temperature Sensor
	if data.Temperature != nil {
		envelopes = append(envelopes, &envelope.EventEnvelope{
			Payload: &envelope.EventEnvelope_Sensor{
				Sensor: &sensor.SensorEvent{
					Id:           deviceID + "_temperature",
					Source:       "zigbee",
					EntityId:     deviceID,
					Value:        fmt.Sprintf("%f", *data.Temperature),
					NumericValue: *data.Temperature,
					Unit:         "°C",
				},
			},
		})
	}

	if len(envelopes) == 0 {
		// Fallback for discovery mode
		slog.Info("DISCOVERY MODE: Unmapped Z2M payload", "topic", topic, "deviceID", deviceID, "payload", string(payload))
		return "zigbee", deviceID, nil
	}

	return "zigbee", deviceID, envelopes
}
