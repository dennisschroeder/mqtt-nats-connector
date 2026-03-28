package logic

import (
	"testing"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
)

func TestFritzTransformer(t *testing.T) {
	transformer := &FritzTransformer{}

	if !transformer.Accepts("fritz-presence-bridge/sensor/state") {
		t.Error("Expected to accept fritz-presence-bridge/ topic")
	}

	src, devID, env := transformer.Transform("fritz-presence-bridge/binary_sensor/juergen_iphone/state", []byte("ON"))
	if src != "fritz-presence-bridge" {
		t.Errorf("got source %q", src)
	}
	if devID != "juergen_iphone" {
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
