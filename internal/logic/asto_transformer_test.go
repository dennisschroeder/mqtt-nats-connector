package logic

import (
	"testing"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
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
	p, ok := env.Payload.(*envelope.EventEnvelope_BinarySensor)
	if !ok {
		t.Fatalf("expected BinarySensor payload")
	}
	if p.BinarySensor.State != common.BinaryState_BINARY_STATE_ON {
		t.Errorf("expected ON state, got %v", p.BinarySensor.State)
	}
}
