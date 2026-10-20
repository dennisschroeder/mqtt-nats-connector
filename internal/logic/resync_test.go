package logic

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	"google.golang.org/protobuf/proto"
)

func newTestService() (*Service, *fakeNATS) {
	n := newFakeNATS()
	return NewService(&fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}, n, nil), n
}

func decodeEvent(t *testing.T, data []byte) *envelope.EventEnvelope {
	t.Helper()
	var env envelope.EventEnvelope
	if err := proto.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	return &env
}

// With z2m retain enabled the last button press stays in the device's retained
// state payload; every resubscribe would otherwise ring the doorbell again.
func TestRetainedActionPayloadDoesNotTriggerEvent(t *testing.T) {
	for _, payload := range []string{
		`{"action":"pressed","battery":90}`,
		`{"battery":90,"linkquality":120}`, // doorbell device without action
		`pressed`,                          // raw action string
	} {
		svc, n := newTestService()
		svc.handleMQTTMessage("zigbee/xx_doorbell", []byte(payload), true)
		if len(n.subjects) != 0 || len(n.kv) != 0 {
			t.Errorf("retained %s produced events %v / KV %v", payload, n.subjects, n.kv)
		}
	}

	// The same payload live is a real press.
	svc, n := newTestService()
	svc.handleMQTTMessage("zigbee/xx_doorbell", []byte(`{"action":"pressed"}`), false)
	if want := []string{"iot.v1.events.zigbee.global.binary_sensor.xx_doorbell"}; !slices.Equal(n.subjects, want) {
		t.Errorf("live press published %v, want %v", n.subjects, want)
	}
}

// A retained payload can hold both an old action and current state; only the
// state may be resynced.
func TestRetainedPayloadResyncsStateButNotAction(t *testing.T) {
	svc, n := newTestService()
	svc.handleMQTTMessage("zigbee/xx_remote", []byte(`{"action":"on","state":"ON"}`), true)

	if want := []string{"iot.v1.events.zigbee.global.light.xx_remote"}; !slices.Equal(n.subjects, want) {
		t.Errorf("published %v, want only the light state %v", n.subjects, want)
	}
	if want := map[string]string{"light.xx_remote": "ON"}; !maps.Equal(n.kv, want) {
		t.Errorf("KV = %v, want %v", n.kv, want)
	}
}

// zwave-js-ui retains the write-only On/Off level-change buttons next to
// currentValue; only currentValue is state.
func TestRetainedZWaveButtonsAreIgnored(t *testing.T) {
	svc, n := newTestService()
	n.kv["light.ro_light_1"] = "ON"
	for _, topic := range []string{
		"zwave/ro_light_1/switch_multilevel/endpoint_1/On",
		"zwave/ro_light_1/switch_multilevel/endpoint_1/Off",
	} {
		svc.handleMQTTMessage(topic, []byte(`{"time":1,"value":false}`), true)
	}
	if len(n.subjects) != 0 || n.kv["light.ro_light_1"] != "ON" {
		t.Errorf("retained buttons produced events %v / KV %v", n.subjects, n.kv)
	}

	svc.handleMQTTMessage("zwave/ro_light_1/switch_multilevel/endpoint_1/currentValue", []byte(`{"time":1,"value":0}`), true)
	if n.kv["light.ro_light_1"] != "OFF" {
		t.Errorf("retained currentValue not resynced, KV = %v", n.kv)
	}
}

func TestRetainedStateResync(t *testing.T) {
	const topic = "ccu2/switch/hm_light/state"
	const key = "binary_sensor.hm_light"

	t.Run("unchanged state is skipped", func(t *testing.T) {
		svc, n := newTestService()
		n.kv[key] = "ON"
		svc.handleMQTTMessage(topic, []byte("ON"), true)
		if len(n.subjects) != 0 {
			t.Errorf("unchanged retained state published %v", n.subjects)
		}
		if svc.resyncUnchanged != 1 {
			t.Errorf("resyncUnchanged = %d, want 1", svc.resyncUnchanged)
		}
	})

	for name, kv := range map[string]map[string]string{
		"changed state": {key: "OFF"},
		"unknown key":   {},
	} {
		t.Run(name+" is published as missed change", func(t *testing.T) {
			svc, n := newTestService()
			maps.Copy(n.kv, kv)
			svc.handleMQTTMessage(topic, []byte("ON"), true)
			if want := []string{"iot.v1.events.ccu2.global.binary_sensor.hm_light"}; !slices.Equal(n.subjects, want) {
				t.Fatalf("published %v, want %v", n.subjects, want)
			}
			if id := decodeEvent(t, n.events[0]).Id; !strings.HasPrefix(id, "resync_") {
				t.Errorf("event id %q, want resync_ prefix", id)
			}
			if n.kv[key] != "ON" {
				t.Errorf("KV = %v, want %s=ON", n.kv, key)
			}
		})
	}

	t.Run("KV read error updates state without event", func(t *testing.T) {
		svc, n := newTestService()
		n.kvGetErr = errors.New("nats: timeout")
		svc.handleMQTTMessage(topic, []byte("ON"), true)
		if len(n.subjects) != 0 {
			t.Errorf("published %v despite KV read error", n.subjects)
		}
		if n.kv[key] != "ON" {
			t.Errorf("KV = %v, want %s=ON", n.kv, key)
		}
	})

	t.Run("live message is always published", func(t *testing.T) {
		svc, n := newTestService()
		n.kv[key] = "ON"
		svc.handleMQTTMessage(topic, []byte("ON"), false)
		if len(n.subjects) != 1 || !strings.HasPrefix(decodeEvent(t, n.events[0]).Id, "evt_") {
			t.Errorf("live message published %v", n.subjects)
		}
	})
}

// The retained flag must survive the hop through the inbox.
func TestRunPassesRetainedFlagThrough(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}
	n := newFakeNATS()
	n.kv["binary_sensor.hm_light"] = "ON"
	svc := NewService(m, n, []string{"ccu2/#"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- svc.Run(ctx) }()
	<-n.subscribed

	m.handlers["ccu2/#"]("ccu2/switch/hm_light/state", []byte("ON"), true)
	m.handlers["ccu2/#"]("zigbee/xx_doorbell", []byte(`{"action":"pressed"}`), true)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(n.subjects) != 0 {
		t.Errorf("retained snapshot published %v", n.subjects)
	}
}
