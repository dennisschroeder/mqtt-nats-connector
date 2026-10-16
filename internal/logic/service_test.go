package logic

import (
	"context"
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
	mu        sync.Mutex
	handlers  map[string]mqtt.MessageHandler
	published []string
}

func (f *fakeMQTT) Subscribe(topic string, handler mqtt.MessageHandler) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[topic] = handler
	return nil
}

func (f *fakeMQTT) Publish(topic string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, topic)
	return nil
}

type fakeNATS struct {
	actionHandler natsgo.MsgHandler
	subscribed    chan struct{}
}

func (f *fakeNATS) Publish(string, []byte) error { return nil }
func (f *fakeNATS) PutKV(string, []byte) error   { return nil }

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
// write" in production (16.10.2026).
func TestServiceConcurrentEventsAndActions(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}
	n := &fakeNATS{subscribed: make(chan struct{})}
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
		ccu := m.handlers["ccu2/#"]
		z2m := m.handlers["zigbee/#"]
		for range iterations {
			ccu("ccu2/cover/ba_gf_shutter_1/state", []byte("ON"))
			z2m("zigbee/bridge/groups", []byte(`[{"friendly_name":"lr_lights","scenes":[{"id":3,"name":"evening"}]}]`))
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

	// With the cache warm, the cover command must go to the CCU only.
	m.published = m.published[:0]
	n.actionHandler(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: actions[0]})
	if len(m.published) != 1 || m.published[0] != "ccu2/cover/ba_gf_shutter_1/set" {
		t.Errorf("cover action published to %v, want [ccu2/cover/ba_gf_shutter_1/set]", m.published)
	}
}
