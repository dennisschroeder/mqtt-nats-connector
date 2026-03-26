package logic

import (
	"testing"

	iotv1 "github.com/dennisschroeder/iot-schemas-proto/gen/go/iot/v1"
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
	p, ok := env.Payload.(*iotv1.EventEnvelope_Presence)
	if !ok {
		t.Fatalf("expected Presence payload")
	}
	if p.Presence.State != iotv1.BinaryState_BINARY_STATE_ON {
		t.Errorf("expected ON state, got %v", p.Presence.State)
	}
}