package logic

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/action"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/cover"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/light"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/scene"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	natsgo "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

type fakeMQTT struct {
	mu             sync.Mutex
	handlers       map[string]mqtt.MessageHandler
	published      []string // "topic payload"
	closed         bool
	panicOnPublish bool
}

func (f *fakeMQTT) Subscribe(topic string, handler mqtt.MessageHandler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[topic] = handler
	return nil
}

func (f *fakeMQTT) Publish(topic string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicOnPublish {
		panic("mqtt publish failed")
	}
	f.published = append(f.published, topic+" "+string(payload))
	return nil
}

func (f *fakeMQTT) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

type fakeNATS struct {
	actionHandler  natsgo.MsgHandler
	subscribed     chan struct{}
	mu             sync.Mutex
	subjects       []string
	kv             map[string]string
	panicOnPublish bool
}

func newFakeNATS() *fakeNATS {
	return &fakeNATS{subscribed: make(chan struct{}), kv: map[string]string{}}
}

func (f *fakeNATS) Publish(subject string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicOnPublish {
		panic("nats publish failed")
	}
	f.subjects = append(f.subjects, subject)
	return nil
}

func (f *fakeNATS) PutKV(key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kv[key] = string(data)
	return nil
}

func (f *fakeNATS) Subscribe(_ string, handler natsgo.MsgHandler) (*natsgo.Subscription, error) {
	f.actionHandler = handler
	close(f.subscribed)
	return nil, nil
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Paho delivers MQTT messages and nats.go delivers action messages on different
// goroutines, so the caches shared between both paths must be synchronized.
// Run with -race: before the fix this tripped "concurrent map read and map
// write" in production (2026-10-15 23:40Z).
func TestServiceConcurrentEventsAndActions(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}
	n := newFakeNATS()
	svc := NewService(m, n, []string{"ccu2/#", "zigbee/#"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	<-n.subscribed

	pos := int32(0)
	actions := [][]byte{
		mustMarshal(t, &action.ActionRequest{
			Id: "cover", TargetEntity: "ba_gf_shutter_1",
			Command: &action.ActionRequest_Cover{Cover: &cover.CoverCommand{State: "CLOSE", Position: &pos}},
		}),
		mustMarshal(t, &action.ActionRequest{
			Id: "light", TargetEntity: "ba_gf_shutter_1",
			Command: &action.ActionRequest_Light{Light: &light.LightCommand{State: common.BinaryState_BINARY_STATE_ON}},
		}),
		mustMarshal(t, &action.ActionRequest{
			Id: "scene", TargetEntity: "lr_lights",
			Command: &action.ActionRequest_Scene{Scene: &scene.SceneCommand{SceneId: "evening"}},
		}),
	}

	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Call the handler directly: the subscribed callback only enqueues.
		for range iterations {
			svc.handleMQTTMessage("ccu2/cover/ba_gf_shutter_1/state", []byte("ON"))
			svc.handleMQTTMessage("zigbee/bridge/groups", []byte(`[{"friendly_name":"lr_lights","scenes":[{"id":3,"name":"evening"}]}]`))
		}
	}()
	go func() {
		defer wg.Done()
		for range iterations {
			for _, data := range actions {
				n.actionHandler(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: data})
			}
		}
	}()
	wg.Wait()

	// With the caches warm, the cover command goes to the CCU only and the
	// scene name resolves to the Z2M scene ID.
	m.published = m.published[:0]
	n.actionHandler(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: actions[0]})
	if want := []string{"ccu2/cover/ba_gf_shutter_1/set 0"}; !slices.Equal(m.published, want) {
		t.Errorf("cover action published %v, want %v", m.published, want)
	}

	m.published = m.published[:0]
	n.actionHandler(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: actions[2]})
	if want := `zigbee/lr_lights/set {"scene_recall":3}`; !slices.Contains(m.published, want) {
		t.Errorf("scene action published %v, want it to contain %q", m.published, want)
	}
}

// The connector's own commands (ccu2/cover/<id>/set) come back via the ccu2/#
// subscription and must not become binary_sensor events or KV state.
func TestServiceIgnoresCommandTopics(t *testing.T) {
	n := newFakeNATS()
	svc := NewService(&fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}, n, nil)

	for _, topic := range []string{
		"ccu2/cover/OEQ1219312_1/set",
		"zigbee/kt_island/set",
		"zigbee/kt_island/set/brightness",
		"zwave/lr_dimmer/switch_multilevel/endpoint_1/targetValue/set",
	} {
		svc.handleMQTTMessage(topic, []byte("CLOSE"))
	}
	if len(n.subjects) != 0 || len(n.kv) != 0 {
		t.Errorf("command topics produced events %v / KV %v", n.subjects, n.kv)
	}
	if _, ok := svc.cachedSource("OEQ1219312_1"); ok {
		t.Error("command topic populated the source cache")
	}

	svc.handleMQTTMessage("ccu2/cover/OEQ1219312_1/state", []byte("ON"))
	if want := "binary_sensor.OEQ1219312_1"; n.kv[want] != "ON" {
		t.Errorf("state topic not mirrored, KV = %v", n.kv)
	}
}

func TestHandlersRecoverFromPanics(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}, panicOnPublish: true}
	n := newFakeNATS()
	n.panicOnPublish = true
	svc := NewService(m, n, nil)

	data := mustMarshal(t, &action.ActionRequest{
		Id: "light", TargetEntity: "kt_island",
		Command: &action.ActionRequest_Light{Light: &light.LightCommand{State: common.BinaryState_BINARY_STATE_ON}},
	})
	svc.handleAction(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: data})
	svc.handleMQTTMessage("ccu2/cover/x/state", []byte("ON"))

	// Handlers stay usable after a panic.
	m.panicOnPublish = false
	svc.handleAction(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: data})
	if len(m.published) == 0 {
		t.Error("action after recovered panic was not published")
	}
}

// Messages are acked as soon as they are queued, so everything queued must
// still be processed, in order, when the pod shuts down.
func TestRunProcessesQueuedMessagesInOrderOnShutdown(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}
	n := newFakeNATS()
	svc := NewService(m, n, []string{"ccu2/#"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- svc.Run(ctx) }()
	<-n.subscribed

	const count = 500
	var want []string
	for i := range count {
		dev := fmt.Sprintf("dev%d", i)
		m.handlers["ccu2/#"]("ccu2/contact/"+dev+"/state", []byte("ON"))
		want = append(want, "iot.v1.events.ccu2.global.binary_sensor."+dev)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(n.subjects, want) {
		t.Errorf("got %d events (first %v), want %d in order", len(n.subjects), n.subjects[:min(3, len(n.subjects))], count)
	}
	if !m.closed {
		t.Error("MQTT client not disconnected on shutdown")
	}
}
