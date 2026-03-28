package logic

import (
	"testing"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
)

func TestHomematicTransformer(t *testing.T) {
	transformer := &HomematicTransformer{}

	if !transformer.Accepts("ccu2/cover/OEQ1219312_1/position") {
		t.Error("Expected to accept ccu2/ topic")
	}

	src, devID, env := transformer.Transform("ccu2/cover/OEQ1219312_1/position", []byte("100"))
	if src != "ccu2" {
		t.Errorf("got source %q", src)
	}
	if devID != "OEQ1219312_1" {
		t.Errorf("got deviceID %q", devID)
	}
	if env == nil {
		t.Fatal("expected envelope")
	}
	p, ok := env.Payload.(*envelope.EventEnvelope_BinarySensor)
	if !ok {
		t.Fatalf("expected BinarySensor payload")
	}
	if p.BinarySensor.State != common.BinaryState_BINARY_STATE_OFF {
		t.Errorf("expected OFF state for non-ON value, got %v", p.BinarySensor.State)
	}
}
