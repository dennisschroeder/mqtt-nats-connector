package logic

import (
	"testing"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

func TestHomematicTransformer(t *testing.T) {
	transformer := &HomematicTransformer{}

	if !transformer.Accepts("homematic-bridge/cover/OEQ1219312_1/position") {
		t.Error("Expected to accept homematic-bridge/ topic")
	}

	src, devID, env := transformer.Transform("homematic-bridge/cover/OEQ1219312_1/position", []byte("100"))
	if src != "homematic-bridge" {
		t.Errorf("got source %q", src)
	}
	if devID != "OEQ1219312_1" {
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