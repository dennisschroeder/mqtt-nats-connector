package logic

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/action"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/scene"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	natsgo "github.com/nats-io/nats.go"
)

// Payload as published by z2m 2.14.2: the logger prefixes the namespace.
const noChannelAccessLog = `{"message":"z2m: Publish 'set' 'scene_recall' to 'kt_lights' failed: 'Error: Command 6 genScenes.recall({\"groupid\":6,\"sceneid\":2}) failed (Failed APS-DATA.request with confirm status: MacNoChannelAccess (0xe1))'","level":"error"}`

func TestParseZ2MPublishFailure(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		ok        bool
		key       string
		target    string
		transient bool
	}{
		{name: "no channel access", payload: noChannelAccessLog, ok: true, key: "scene_recall", target: "kt_lights", transient: true},
		{
			name:    "channel access failure with namespace field",
			payload: `{"level":"error","namespace":"z2m","message":"Publish 'set' 'scene_recall' to 'lr_lights' failed: 'Error: ZCL command failed (MacChannelAccessFailure (0xe1))'"}`,
			ok:      true, key: "scene_recall", target: "lr_lights", transient: true,
		},
		{
			name:    "non-transient error",
			payload: `{"level":"error","message":"z2m: Publish 'set' 'scene_recall' to 'kt_lights' failed: 'Error: ZCL command 0x1/kt_lights genScenes.recall failed (Status 'NOT_FOUND')'"}`,
			ok:      true, key: "scene_recall", target: "kt_lights", transient: false,
		},
		{
			name:    "other key",
			payload: `{"level":"error","message":"z2m: Publish 'set' 'state' to 'kt_island' failed: 'Error: MacNoChannelAccess'"}`,
			ok:      true, key: "state", target: "kt_island", transient: true,
		},
		{name: "info level", payload: `{"level":"info","message":"z2m: Publish 'set' 'scene_recall' to 'kt_lights' failed: 'MacNoChannelAccess'"}`},
		{name: "unrelated error", payload: `{"level":"error","message":"z2m: MQTT error: connect ECONNREFUSED"}`},
		{name: "not json", payload: `MacNoChannelAccess`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := parseZ2MPublishFailure([]byte(tt.payload))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if f.Key != tt.key || f.Target != tt.target || f.transient() != tt.transient {
				t.Errorf("got key %q target %q transient %v, want %q %q %v", f.Key, f.Target, f.transient(), tt.key, tt.target, tt.transient)
			}
		})
	}
}

func TestTopicMatches(t *testing.T) {
	for _, tt := range []struct {
		filter string
		want   bool
	}{
		{"zigbee/#", true},
		{"#", true},
		{"zigbee/bridge/#", true},
		{"zigbee/+/logging", true},
		{"zigbee/bridge/logging", true},
		{"zigbee/+", false},
		{"zigbee/bridge/logging/x", false},
		{"zwave/#", false},
	} {
		if got := topicMatches(tt.filter, z2mLoggingTopic); got != tt.want {
			t.Errorf("topicMatches(%q) = %v, want %v", tt.filter, got, tt.want)
		}
	}
}

type fakeTimer struct {
	delay   time.Duration
	fn      func()
	stopped bool
}

func (f *fakeTimer) Stop() bool {
	f.stopped = true
	return true
}

type retryHarness struct {
	r         *sceneRetrier
	mu        sync.Mutex
	now       time.Time
	timers    []*fakeTimer
	published []string
}

func newRetryHarness() *retryHarness {
	h := &retryHarness{now: time.Unix(1_800_000_000, 0)}
	h.r = newSceneRetrier(DefaultSceneRetryConfig(), func(topic string, payload []byte) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.published = append(h.published, topic+" "+string(payload))
		return nil
	})
	h.r.now = func() time.Time { return h.now }
	h.r.afterFunc = func(d time.Duration, fn func()) stopper {
		t := &fakeTimer{delay: d, fn: fn}
		h.timers = append(h.timers, t)
		return t
	}
	return h
}

// fire runs the latest scheduled retry after advancing the clock by its delay.
func (h *retryHarness) fire(t *testing.T) {
	t.Helper()
	if len(h.timers) == 0 {
		t.Fatal("no retry scheduled")
	}
	timer := h.timers[len(h.timers)-1]
	h.now = h.now.Add(timer.delay)
	timer.fn()
}

func TestSceneRetryRetriesTransientFailure(t *testing.T) {
	h := newRetryHarness()
	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":2}`))

	h.r.handleLog([]byte(noChannelAccessLog))
	if len(h.timers) != 1 || h.timers[0].delay != 200*time.Millisecond {
		t.Fatalf("want one retry after 200ms, got %+v", h.timers)
	}
	// Repeated reports before the retry went out must not stack retries.
	h.r.handleLog([]byte(noChannelAccessLog))
	if len(h.timers) != 1 {
		t.Fatalf("duplicate failure scheduled another retry: %d timers", len(h.timers))
	}

	h.fire(t)
	if want := []string{`zigbee/kt_lights/set {"scene_recall":2}`}; !slices.Equal(h.published, want) {
		t.Errorf("published %v, want %v", h.published, want)
	}
}

func TestSceneRetryStopsAfterMaxRetries(t *testing.T) {
	h := newRetryHarness()
	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":2}`))

	for _, wantDelay := range []time.Duration{200 * time.Millisecond, 500 * time.Millisecond} {
		h.r.handleLog([]byte(noChannelAccessLog))
		if got := h.timers[len(h.timers)-1].delay; got != wantDelay {
			t.Fatalf("retry delay %v, want %v", got, wantDelay)
		}
		h.fire(t)
	}
	h.r.handleLog([]byte(noChannelAccessLog))

	if len(h.timers) != 2 || len(h.published) != 2 {
		t.Errorf("want 2 retries, got %d scheduled / %d published", len(h.timers), len(h.published))
	}
	if _, ok := h.r.recalls["kt_lights"]; ok {
		t.Error("recall still remembered after giving up")
	}
}

func TestSceneRetryIgnoresNonTransientAndUnknownFailures(t *testing.T) {
	h := newRetryHarness()
	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":2}`))

	h.r.handleLog([]byte(`{"level":"error","message":"z2m: Publish 'set' 'scene_recall' to 'lr_lights' failed: 'Error: MacNoChannelAccess'"}`))
	h.r.handleLog([]byte(`{"level":"error","message":"z2m: Publish 'set' 'state' to 'kt_lights' failed: 'Error: MacNoChannelAccess'"}`))
	h.r.handleLog([]byte(`{"level":"error","message":"z2m: Publish 'set' 'scene_recall' to 'kt_lights' failed: 'Error: Status 'NOT_FOUND''"}`))

	if len(h.timers) != 0 {
		t.Errorf("scheduled %d retries, want none", len(h.timers))
	}
}

func TestSceneRetryExpires(t *testing.T) {
	h := newRetryHarness()
	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":2}`))
	h.now = h.now.Add(5*time.Second + time.Millisecond)

	h.r.handleLog([]byte(noChannelAccessLog))
	if len(h.timers) != 0 {
		t.Errorf("failure after the window scheduled a retry")
	}

	// A retry scheduled in time is dropped if the window elapses before it fires.
	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":2}`))
	h.now = h.now.Add(4900 * time.Millisecond)
	h.r.handleLog([]byte(noChannelAccessLog))
	h.fire(t)
	if len(h.published) != 0 {
		t.Errorf("expired retry published %v", h.published)
	}
}

func TestSceneRetryNewRecallSupersedesPendingRetry(t *testing.T) {
	h := newRetryHarness()
	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":2}`))
	h.r.handleLog([]byte(noChannelAccessLog))
	old := h.timers[0]

	h.r.remember("kt_lights", "zigbee/kt_lights/set", []byte(`{"scene_recall":3}`))
	if !old.stopped {
		t.Error("pending retry of the superseded recall not stopped")
	}
	old.fn() // a timer that already fired must not resend the old scene
	if len(h.published) != 0 {
		t.Errorf("superseded recall was resent: %v", h.published)
	}
}

// End to end through the service: the scene action is remembered and a z2m
// failure on zigbee/bridge/logging resends it without emitting NATS events.
func TestServiceRetriesSceneRecallOnZ2MFailure(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}
	n := newFakeNATS()
	svc := NewService(m, n, []string{"ccu2/#"})
	svc.SetSceneRetry(SceneRetryConfig{Delays: []time.Duration{time.Millisecond}, MaxRetries: 2, Window: 5 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	<-n.subscribed
	m.mu.Lock()
	_, subscribed := m.handlers[z2mLoggingTopic]
	m.mu.Unlock()
	if !subscribed {
		t.Fatalf("not subscribed to %s when no mirrored topic covers it", z2mLoggingTopic)
	}

	n.actionHandler(&natsgo.Msg{Subject: "iot.v1.actions.test", Data: mustMarshal(t, &action.ActionRequest{
		Id: "scene", TargetEntity: "kt_lights",
		Command: &action.ActionRequest_Scene{Scene: &scene.SceneCommand{SceneId: "2"}},
	})})
	svc.handleMQTTMessage(z2mLoggingTopic, []byte(noChannelAccessLog), false)

	recall := `zigbee/kt_lights/set {"scene_recall":2}`
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		count := 0
		for _, p := range m.published {
			if p == recall {
				count++
			}
		}
		m.mu.Unlock()
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scene recall published %d times, want original + 1 retry", count)
		}
		time.Sleep(time.Millisecond)
	}
	if len(n.subjects) != 0 {
		t.Errorf("z2m logging produced NATS events %v", n.subjects)
	}
}

func TestServiceDoesNotDoubleSubscribeZ2MLogging(t *testing.T) {
	m := &fakeMQTT{handlers: map[string]mqtt.MessageHandler{}}
	n := newFakeNATS()
	svc := NewService(m, n, []string{"zigbee/#"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()
	<-n.subscribed

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.handlers[z2mLoggingTopic]; ok {
		t.Errorf("subscribed to %s although zigbee/# covers it", z2mLoggingTopic)
	}
}
