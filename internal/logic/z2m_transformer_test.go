package logic

import (
	"testing"

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
