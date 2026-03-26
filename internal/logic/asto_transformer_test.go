package logic

import (
	"testing"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
)

func TestAstoTransformer(t *testing.T) {
	transformer := &AstoTransformer{}

	if !transformer.Accepts("asto-waste-bridge/sensor/state") {
		t.Error("Expected to accept asto-waste-bridge/ topic")
	}

	src, devID, env := transformer.Transform("asto-waste-bridge/sensor/residual_waste/state", []byte("ON"))
	if src != "asto-waste-bridge" {
		t.Errorf("got source %q", src)
	}
	if devID != "residual_waste" {
		t.Errorf("got deviceID %q", devID)
	}
	if env == nil {
		t.Fatal("expected envelope")
	}
	p, ok := env.Payload.(*iotv1.EventEnvelope_Presence)
	if !ok {
		t.Fatalf("expected Presence payload")
	}
	if p.Presence.State != iotv1.BinaryState_BINARY_STATE_ON {
		t.Errorf("expected ON state, got %v", p.Presence.State)
	}
}