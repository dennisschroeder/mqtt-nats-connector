package logic

import (
	"testing"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

func TestStiebelTransformer(t *testing.T) {
	transformer := &StiebelTransformer{}

	if !transformer.Accepts("stiebel-modbus2mqtt-go/sensor/bo_heatpump_register_509/state") {
		t.Error("Expected to accept stiebel-modbus2mqtt-go/ topic")
	}

	src, devID, env := transformer.Transform("stiebel-modbus2mqtt-go/sensor/bo_heatpump_register_509/state", []byte("38.4"))
	if src != "stiebel-modbus2mqtt-go" {
		t.Errorf("got source %q", src)
	}
	if devID != "bo_heatpump_register_509" {
		t.Errorf("got deviceID %q", devID)
	}
	if env == nil {
		t.Fatal("expected envelope")
	}
	p, ok := env.Payload.(*iotv1.EventEnvelope_Presence)
	if !ok {
		t.Fatalf("expected Presence payload")
	}
	if p.Presence.State != iotv1.BinaryState_BINARY_STATE_OFF {
		t.Errorf("expected OFF state for non-ON value, got %v", p.Presence.State)
	}
}