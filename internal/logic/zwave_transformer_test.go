package logic

import (
	"testing"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

func TestZWaveTransformer(t *testing.T) {
	transformer := &ZWaveTransformer{}

	if !transformer.Accepts("zwave/my_device") {
		t.Error("Expected to accept zwave/ topic")
	}
	if transformer.Accepts("zigbee/my_device") {
		t.Error("Expected not to accept zigbee/ topic")
	}

	tests := []struct {
		name         string
		topic        string
		payload      []byte
		wantSource   string
		wantDeviceID string
		wantPayload  interface{}
		checkState   iotv1.BinaryState
	}{
		{
			name:         "Switch On",
			topic:        "zwave/me_light_1/switch_multilevel/endpoint_2/On",
			payload:      []byte(`{"time":1774310506562}`),
			wantSource:   "zwave",
			wantDeviceID: "me_light_1",
			wantPayload:  &iotv1.EventEnvelope_Light{},
			checkState:   iotv1.BinaryState_BINARY_STATE_ON,
		},
		{
			name:         "Switch Off",
			topic:        "zwave/me_light_1/switch_multilevel/endpoint_2/Off",
			payload:      []byte(`{"time":1774310506564}`),
			wantSource:   "zwave",
			wantDeviceID: "me_light_1",
			wantPayload:  &iotv1.EventEnvelope_Light{},
			checkState:   iotv1.BinaryState_BINARY_STATE_OFF,
		},
		{
			name:         "Current Value (Brightness)",
			topic:        "zwave/me_light_1/switch_multilevel/endpoint_2/currentValue",
			payload:      []byte(`{"value":50.0}`),
			wantSource:   "zwave",
			wantDeviceID: "me_light_1",
			wantPayload:  &iotv1.EventEnvelope_Light{},
			checkState:   iotv1.BinaryState_BINARY_STATE_ON,
		},
		{
			name:         "Unknown endpoint (Discovery)",
			topic:        "zwave/me_light_1/manufacturer_specific/endpoint_0/productId",
			payload:      []byte(`{"value":4096}`),
			wantSource:   "zwave",
			wantDeviceID: "me_light_1",
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
				switch tt.wantPayload.(type) {
				case *iotv1.EventEnvelope_Light:
					l, ok := env.Payload.(*iotv1.EventEnvelope_Light)
					if !ok {
						t.Errorf("expected Light payload, got %T", env.Payload)
					} else {
						if l.Light.State != tt.checkState {
							t.Errorf("got state %v, want %v", l.Light.State, tt.checkState)
						}
					}
				}
			}
		})
	}
}