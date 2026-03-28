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
		wantPayload  interface{} // nil means we expect envelope or payload to be nil
	}{
		{
			name:         "Motion event",
			topic:        "zigbee/living_room_motion",
			payload:      []byte(`{"occupancy":true}`),
			wantSource:   "zigbee",
			wantDeviceID: "living_room_motion",
			wantPayload:  &envelope.EventEnvelope_BinarySensor{},
		},
		{
			name:         "Light event",
			topic:        "zigbee/living_room_light",
			payload:      []byte(`{"state":"ON","brightness":128}`),
			wantSource:   "zigbee",
			wantDeviceID: "living_room_light",
			wantPayload:  &envelope.EventEnvelope_Light{},
		},
		{
			name:         "Availability event (ignored)",
			topic:        "zigbee/living_room_light/availability",
			payload:      []byte(`{"state":"online"}`),
			wantSource:   "zigbee",
			wantDeviceID: "living_room_light",
			wantPayload:  nil,
		},
		{
			name:         "Bridge event (ignored)",
			topic:        "zigbee/bridge/logging",
			payload:      []byte(`{"level":"info"}`),
			wantSource:   "zigbee",
			wantDeviceID: "bridge",
			wantPayload:  nil,
		},
		{
			name:         "Unknown payload (discovery mode)",
			topic:        "zigbee/unknown_sensor",
			payload:      []byte(`{"temperature":22.5}`),
			wantSource:   "zigbee",
			wantDeviceID: "unknown_sensor",
			wantPayload:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, devID, env := transformer.Transform(tt.topic, tt.payload)
			if src != tt.wantSource {
				t.Errorf("got source %q, want %q", src, tt.wantSource)
			}
			if devID != tt.wantDeviceID {
				t.Errorf("got deviceID %q, want %q", devID, tt.wantDeviceID)
			}
			if tt.wantPayload == nil {
				if env != nil {
					t.Errorf("expected nil envelope, got %v", env)
				}
			} else {
				if env == nil {
					t.Fatalf("expected non-nil envelope")
				}
				// Simple type check for payload
				switch tt.wantPayload.(type) {
				case *envelope.EventEnvelope_BinarySensor:
					if _, ok := env.Payload.(*envelope.EventEnvelope_BinarySensor); !ok {
						t.Errorf("expected BinarySensor payload, got %T", env.Payload)
					}
				case *envelope.EventEnvelope_Light:
					if _, ok := env.Payload.(*envelope.EventEnvelope_Light); !ok {
						t.Errorf("expected Light payload, got %T", env.Payload)
					}
				}
			}
		})
	}
}
