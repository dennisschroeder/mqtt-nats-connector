package logic

import (
	"strings"
	"testing"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
)

func TestZ2MTransformer(t *testing.T) {
	transformer := &Z2MTransformer{}

	if !transformer.Accepts("zigbee/my_device") {
		t.Error("Expected to accept zigbee/ topic")
	}
	if transformer.Accepts("zwave/my_device") {
		t.Error("Expected not to accept zwave/ topic")
	}

	tests := []struct {
		name         string
		topic        string
		payload      []byte
		wantSource   string
		wantDeviceID string
		wantPayloads []interface{} // nil means we expect envelope or payload to be nil
	}{
		{
			name:         "Motion event",
			topic:        "zigbee/living_room_motion",
			payload:      []byte(`{"occupancy":true}`),
			wantSource:   "zigbee",
			wantDeviceID: "living_room_motion",
			wantPayloads: []interface{}{&envelope.EventEnvelope_BinarySensor{}},
		},
		{
			name:         "Light event",
			topic:        "zigbee/living_room_light",
			payload:      []byte(`{"state":"ON","brightness":128}`),
			wantSource:   "zigbee",
			wantDeviceID: "living_room_light",
			wantPayloads: []interface{}{&envelope.EventEnvelope_Light{}},
		},
		{
			name:         "Availability event (ignored)",
			topic:        "zigbee/living_room_light/availability",
			payload:      []byte(`{"state":"online"}`),
			wantSource:   "zigbee",
			wantDeviceID: "living_room_light",
			wantPayloads: nil,
		},
		{
			name:         "Bridge event (ignored)",
			topic:        "zigbee/bridge/logging",
			payload:      []byte(`{"level":"info"}`),
			wantSource:   "zigbee",
			wantDeviceID: "bridge",
			wantPayloads: nil,
		},
		{
			name:         "Unknown payload (discovery mode)",
			topic:        "zigbee/unknown_sensor",
			payload:      []byte(`{"pressure":1013}`),
			wantSource:   "zigbee",
			wantDeviceID: "unknown_sensor",
			wantPayloads: nil,
		},
		{
			name:         "Temperature payload",
			topic:        "zigbee/temp_sensor",
			payload:      []byte(`{"temperature":22.5}`),
			wantSource:   "zigbee",
			wantDeviceID: "temp_sensor",
			wantPayloads: []interface{}{&envelope.EventEnvelope_Sensor{}},
		},
		{
			name:         "Illuminance payload",
			topic:        "zigbee/light_sensor",
			payload:      []byte(`{"illuminance":650}`),
			wantSource:   "zigbee",
			wantDeviceID: "light_sensor",
			wantPayloads: []interface{}{&envelope.EventEnvelope_Sensor{}},
		},
		{
			name:         "Combined Motion and Illuminance",
			topic:        "zigbee/motion_sensor_combo",
			payload:      []byte(`{"occupancy":true,"illuminance":300}`),
			wantSource:   "zigbee",
			wantDeviceID: "motion_sensor_combo",
			wantPayloads: []interface{}{&envelope.EventEnvelope_BinarySensor{}, &envelope.EventEnvelope_Sensor{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, devID, envs := transformer.TransformMulti(tt.topic, tt.payload)
			if src != tt.wantSource {
				t.Errorf("got source %q, want %q", src, tt.wantSource)
			}
			if devID != tt.wantDeviceID {
				t.Errorf("got deviceID %q, want %q", devID, tt.wantDeviceID)
			}
			if tt.wantPayloads == nil {
				if len(envs) > 0 {
					t.Errorf("expected nil envelopes, got %v", envs)
				}
			} else {
				if len(envs) != len(tt.wantPayloads) {
					t.Fatalf("expected %d envelopes, got %d", len(tt.wantPayloads), len(envs))
				}
				for i, env := range envs {
					if env == nil {
						t.Fatalf("expected non-nil envelope at index %d", i)
					}
					switch tt.wantPayloads[i].(type) {
					case *envelope.EventEnvelope_BinarySensor:
						if _, ok := env.Payload.(*envelope.EventEnvelope_BinarySensor); !ok {
							t.Errorf("expected BinarySensor payload at index %d, got %T", i, env.Payload)
						}
					case *envelope.EventEnvelope_Light:
						if _, ok := env.Payload.(*envelope.EventEnvelope_Light); !ok {
							t.Errorf("expected Light payload at index %d, got %T", i, env.Payload)
						}
					case *envelope.EventEnvelope_Sensor:
						if _, ok := env.Payload.(*envelope.EventEnvelope_Sensor); !ok {
							t.Errorf("expected Sensor payload at index %d, got %T", i, env.Payload)
						}
					}
				}
			}
		})
	}
}

func TestZ2MTransformerDoorbellSinglePressYieldsOneEvent(t *testing.T) {
	transformer := &Z2MTransformer{}

	// z2m publishes both topics for one press of me_doorbell.
	messages := []struct {
		topic   string
		payload []byte
	}{
		{"zigbee/me_doorbell", []byte(`{"action":"pressed","battery":59,"battery_low":false,"linkquality":255,"tamper":false}`)},
		{"zigbee/me_doorbell/action", []byte(`pressed`)},
	}

	var envs []*envelope.EventEnvelope
	for _, m := range messages {
		_, _, e := transformer.TransformMulti(m.topic, m.payload)
		envs = append(envs, e...)
	}

	if len(envs) != 1 {
		t.Fatalf("expected exactly 1 event for one press, got %d", len(envs))
	}
	bs, ok := envs[0].Payload.(*envelope.EventEnvelope_BinarySensor)
	if !ok {
		t.Fatalf("expected BinarySensor payload, got %T", envs[0].Payload)
	}
	if bs.BinarySensor.EntityId != "me_doorbell" || bs.BinarySensor.DeviceClass != "doorbell" || bs.BinarySensor.State != common.BinaryState_BINARY_STATE_ON {
		t.Errorf("unexpected doorbell event: %+v", bs.BinarySensor)
	}
}

func TestZ2MTransformerIgnoresDoorbellSubtopics(t *testing.T) {
	transformer := &Z2MTransformer{}

	for _, topic := range []string{"zigbee/me_doorbell/action", "zigbee/me_doorbell/availability"} {
		payload := []byte(`pressed`)
		if strings.HasSuffix(topic, "availability") {
			payload = []byte(`{"state":"online"}`)
		}
		if _, _, envs := transformer.TransformMulti(topic, payload); len(envs) != 0 {
			t.Errorf("%s: expected no events, got %d", topic, len(envs))
		}
	}
}

func TestZ2MTransformerDoorbellStatusWithoutActionYieldsOff(t *testing.T) {
	transformer := &Z2MTransformer{}

	_, _, envs := transformer.TransformMulti("zigbee/me_doorbell", []byte(`{"battery":59,"battery_low":false,"last_seen":"2026-10-10T15:30:00+02:00","linkquality":255,"tamper":false}`))

	if len(envs) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(envs))
	}
	bs, ok := envs[0].Payload.(*envelope.EventEnvelope_BinarySensor)
	if !ok {
		t.Fatalf("expected BinarySensor payload, got %T", envs[0].Payload)
	}
	if bs.BinarySensor.EntityId != "me_doorbell" || bs.BinarySensor.DeviceClass != "doorbell" || bs.BinarySensor.State != common.BinaryState_BINARY_STATE_OFF {
		t.Errorf("unexpected doorbell event: %+v", bs.BinarySensor)
	}
}

// z2m contact=true means closed; events follow the HA door/window convention
// where ON means open.
func TestZ2MTransformerContact(t *testing.T) {
	transformer := &Z2MTransformer{}

	tests := []struct {
		deviceID  string
		payload   string
		wantState common.BinaryState
		wantClass string
	}{
		{"be_window_contact", `{"battery_low":false,"contact":true,"linkquality":255,"tamper":false}`, common.BinaryState_BINARY_STATE_OFF, "window"},
		{"be_window_contact", `{"battery_low":false,"contact":false,"linkquality":255,"tamper":false}`, common.BinaryState_BINARY_STATE_ON, "window"},
		{"ha_front_door_contact", `{"contact":false}`, common.BinaryState_BINARY_STATE_ON, "door"},
		{"gr_gate_contact", `{"contact":true}`, common.BinaryState_BINARY_STATE_OFF, "opening"},
	}

	for _, tt := range tests {
		t.Run(tt.deviceID+" "+tt.payload, func(t *testing.T) {
			_, devID, envs := transformer.TransformMulti("zigbee/"+tt.deviceID, []byte(tt.payload))
			if devID != tt.deviceID {
				t.Errorf("got deviceID %q, want %q", devID, tt.deviceID)
			}
			if len(envs) != 1 {
				t.Fatalf("expected exactly 1 event, got %d", len(envs))
			}
			bs := envs[0].GetBinarySensor()
			if bs == nil {
				t.Fatalf("expected BinarySensor payload, got %T", envs[0].Payload)
			}
			if bs.EntityId != tt.deviceID || bs.State != tt.wantState || bs.DeviceClass != tt.wantClass {
				t.Errorf("got %+v, want entity %s state %v class %s", bs, tt.deviceID, tt.wantState, tt.wantClass)
			}
		})
	}
}
