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

// A contact is a state: its retained copy resyncs the KV like any other state.
func TestRetainedContactResync(t *testing.T) {
	const topic = "zigbee/be_window_contact"
	const key = "binary_sensor.be_window_contact"
	closed := []byte(`{"battery_low":false,"contact":true,"linkquality":255,"tamper":false}`)

	svc, n := newTestService()
	svc.handleMQTTMessage(topic, closed, true)
	if want := []string{"iot.v1.events.zigbee.ben.binary_sensor.be_window_contact"}; !slices.Equal(n.subjects, want) {
		t.Fatalf("published %v, want %v", n.subjects, want)
	}
	if n.kv[key] != "OFF" {
		t.Errorf("KV = %v, want %s=OFF", n.kv, key)
	}

	svc.handleMQTTMessage(topic, closed, true)
	if len(n.subjects) != 1 {
		t.Errorf("unchanged retained contact published again: %v", n.subjects)
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

	t.Run("KV read error skips the state", func(t *testing.T) {
		svc, n := newTestService()
		n.kv[key] = "OFF"
		n.kvGetErr = errors.New("nats: timeout")
		svc.handleMQTTMessage(topic, []byte("ON"), true)
		if len(n.subjects) != 0 || n.kv[key] != "OFF" {
			t.Errorf("KV read error produced events %v / KV %v", n.subjects, n.kv)
		}
		if svc.resyncChanged != 0 || svc.resyncFailed != 1 {
			t.Errorf("changed=%d failed=%d, want 0/1", svc.resyncChanged, svc.resyncFailed)
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

// FGD-212 dimmers report the load on endpoint_1 and the S2 channel on
// endpoint_2; both used to share the light.<node> KV key, so every snapshot
// flipped it and produced two phantom resync events.
func TestRetainedZWaveDualEndpointSnapshotIsStable(t *testing.T) {
	svc, n := newTestService()
	snapshot := func() {
		svc.handleMQTTMessage("zwave/ro_light_1/switch_multilevel/endpoint_1/currentValue", []byte(`{"time":1,"value":0}`), true)
		svc.handleMQTTMessage("zwave/ro_light_1/switch_multilevel/endpoint_2/currentValue", []byte(`{"time":1,"value":99}`), true)
	}

	snapshot()
	if n.kv["light.ro_light_1"] != "OFF" || len(n.subjects) != 1 {
		t.Fatalf("first snapshot: KV %v, events %v; want light.ro_light_1=OFF from endpoint_1 and one event", n.kv, n.subjects)
	}

	n.subjects = nil
	snapshot()
	if len(n.subjects) != 0 {
		t.Errorf("identical second snapshot published %v", n.subjects)
	}
}

// Consumers read the KV in their event handlers, so it must be updated first.
func TestKVUpdatedBeforeEventIsPublished(t *testing.T) {
	n := &orderNATS{fakeNATS: newFakeNATS()}
	svc := NewService(&fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}, n, nil)
	svc.handleMQTTMessage("ccu2/switch/hm_light/state", []byte("ON"), false)
	if want := []string{"put", "publish"}; !slices.Equal(n.calls, want) {
		t.Errorf("calls %v, want %v", n.calls, want)
	}
}

type orderNATS struct {
	*fakeNATS
	calls []string
}

func (o *orderNATS) Publish(subject string, data []byte) error {
	o.calls = append(o.calls, "publish")
	return o.fakeNATS.Publish(subject, data)
}

func (o *orderNATS) PutKV(key string, data []byte) error {
	o.calls = append(o.calls, "put")
	return o.fakeNATS.PutKV(key, data)
}
