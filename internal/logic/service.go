package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/action"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/common"
	"github.com/dennisschroeder/iot-schemas-proto/proto/v1/envelope"
	"github.com/dennisschroeder/iot-utils-go/pkg/areas"
	"github.com/dennisschroeder/mqtt-nats-connector/internal/transport/mqtt"
	natsgo "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Transformer interface {
	Accepts(topic string) bool
	Transform(topic string, payload []byte) (source string, deviceID string, envelope *envelope.EventEnvelope)
}

type MultiTransformer interface {
	TransformMulti(topic string, payload []byte) (source string, deviceID string, envelopes []*envelope.EventEnvelope)
}

// mqttClient and natsClient are the subsets of the transport clients the
// service uses, so tests can drive the handlers without a broker.
type mqttClient interface {
	Subscribe(topic string, handler mqtt.MessageHandler) error
	Publish(topic string, payload []byte) error
	Close()
}

type natsClient interface {
	Publish(subject string, data []byte) error
	PutKV(key string, data []byte) error
	GetKV(key string) (value []byte, found bool, err error)
	Subscribe(subject string, handler natsgo.MsgHandler) (*natsgo.Subscription, error)
}

const (
	// Holds the retained snapshot of all mirrored topics on (re)connect.
	inboxSize       = 10000
	shutdownTimeout = 5 * time.Second
)

type mqttMessage struct {
	topic    string
	payload  []byte
	retained bool
}

type Service struct {
	mqtt         mqttClient
	nats         natsClient
	topics       []string
	transformers []Transformer

	// Written from MQTT callbacks, read from NATS action callbacks, which run
	// on different goroutines.
	mu          sync.RWMutex
	sourceCache map[string]string         // deviceID -> source (e.g. "zigbee", "zwave")
	z2mScenes   map[string]map[string]int // TargetEntity -> SceneName -> SceneID

	inbox    chan mqttMessage
	stopping chan struct{}

	// Only touched by the inbox worker.
	resyncChanged, resyncUnchanged, resyncFailed int
}

func NewService(m mqttClient, n natsClient, topics []string) *Service {
	return &Service{
		mqtt:        m,
		nats:        n,
		topics:      topics,
		sourceCache: make(map[string]string),
		z2mScenes:   make(map[string]map[string]int),
		inbox:       make(chan mqttMessage, inboxSize),
		stopping:    make(chan struct{}),
		transformers: []Transformer{
			&Z2MTransformer{},
			&FritzTransformer{},
			&AstoTransformer{},
			&HomematicTransformer{},
			&StiebelTransformer{},
			&ZWaveTransformer{},
			&HAStatestreamTransformer{},
			&ESPHomeTransformer{},
		},
	}
}

func (s *Service) Run(ctx context.Context) error {
	workerDone := make(chan struct{})
	go s.processInbox(workerDone)

	for _, topic := range s.topics {
		slog.Info("Mirroring topic", "topic", topic)
		if err := s.mqtt.Subscribe(topic, s.enqueue); err != nil {
			return err
		}
	}

	// Action Egress: NATS -> MQTT
	sub, err := s.nats.Subscribe("iot.v1.actions.>", s.handleAction)
	if err != nil {
		slog.Error("Failed to subscribe to actions on NATS", "error", err)
		return err
	}

	<-ctx.Done()
	s.shutdown(sub, workerDone)
	return nil
}

// enqueue is the MQTT callback. Paho acks QoS 1 messages only after the
// callback returns, so processing inline throttles the broker; on (re)connect
// the retained snapshot plus the session backlog then exceeds Mosquitto's
// max_queued_messages and messages get dropped.
//
// Deliberate trade-off: the message is acked once it is queued, so queued
// messages are at-most-once on a hard kill (OOM, SIGKILL, power loss); a
// graceful shutdown still processes them. Acking manually after processing
// (SetAutoAckDisabled) would bring back the inflight throttling and overflow.
func (s *Service) enqueue(topic string, payload []byte, retained bool) {
	// Checked first because select picks randomly when the inbox has room too.
	select {
	case <-s.stopping:
		s.dropDuringShutdown(topic)
		return
	default:
	}
	select {
	case s.inbox <- mqttMessage{topic: topic, payload: payload, retained: retained}:
	case <-s.stopping:
		s.dropDuringShutdown(topic)
	}
}

func (s *Service) dropDuringShutdown(topic string) {
	slog.Warn("Not processing MQTT message received during shutdown; the broker redelivers it to the session unless the ack already went out", "topic", topic)
}

// processInbox handles MQTT messages in arrival order on a single goroutine.
func (s *Service) processInbox(done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case m := <-s.inbox:
			s.handleMQTTMessage(m.topic, m.payload, m.retained)
			if len(s.inbox) == 0 {
				s.logResync()
			}
		case <-s.stopping:
			for {
				select {
				case m := <-s.inbox:
					s.handleMQTTMessage(m.topic, m.payload, m.retained)
				default:
					s.logResync()
					return
				}
			}
		}
	}
}

// logResync summarizes a retained snapshot once the burst has been processed.
func (s *Service) logResync() {
	if s.resyncChanged == 0 && s.resyncUnchanged == 0 && s.resyncFailed == 0 {
		return
	}
	slog.Info("Resynced state from retained MQTT snapshot", "changed", s.resyncChanged, "unchanged", s.resyncUnchanged, "failed", s.resyncFailed)
	s.resyncChanged, s.resyncUnchanged, s.resyncFailed = 0, 0, 0
}

// shutdown stops intake on both sides before closing anything, because each
// direction needs the other connection: actions publish to MQTT, events to NATS.
func (s *Service) shutdown(sub *natsgo.Subscription, workerDone <-chan struct{}) {
	slog.Info("Shutting down: draining action subscription")
	if sub != nil {
		if err := sub.Drain(); err != nil {
			slog.Warn("Failed to drain action subscription", "error", err)
		}
		deadline := time.Now().Add(shutdownTimeout)
		for sub.IsValid() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Disconnecting first lets the broker queue new messages in the persistent
	// session for the next pod instead of handing them to this one.
	slog.Info("Shutting down: disconnecting MQTT")
	s.mqtt.Close()

	close(s.stopping)
	select {
	case <-workerDone:
	case <-time.After(shutdownTimeout):
		slog.Warn("Timed out processing queued MQTT messages", "remaining", len(s.inbox))
	}
}

// handleMQTTMessage turns a message into NATS events and KV state.
//
// Retained messages are the broker's snapshot replayed on every (re)subscribe,
// not new occurrences. They only resync state: momentary payloads (button
// actions) are dropped, and a state is published as an event only if it
// differs from the KV, i.e. the change was missed while disconnected.
func (s *Service) handleMQTTMessage(mqttTopic string, payload []byte, retained bool) {
	defer recoverPanic("mqtt message", mqttTopic)

	slog.Debug("Received MQTT message", "topic", mqttTopic, "payload", string(payload), "retained", retained)

	if isCommandTopic(mqttTopic) {
		return
	}

	if mqttTopic == "zigbee/bridge/groups" {
		var groups []struct {
			FriendlyName string `json:"friendly_name"`
			Scenes       []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			} `json:"scenes"`
		}
		if err := json.Unmarshal(payload, &groups); err == nil {
			s.mu.Lock()
			for _, g := range groups {
				if _, ok := s.z2mScenes[g.FriendlyName]; !ok {
					s.z2mScenes[g.FriendlyName] = make(map[string]int)
				}
				for _, sc := range g.Scenes {
					s.z2mScenes[g.FriendlyName][sc.Name] = sc.ID
				}
			}
			s.mu.Unlock()
			slog.Info("Updated Z2M scene mappings", "groups_count", len(groups))
		}
	}

	var transformer Transformer
	for _, t := range s.transformers {
		if t.Accepts(mqttTopic) {
			transformer = t
			break
		}
	}

	if transformer == nil {
		slog.Debug("No transformer found for topic", "topic", mqttTopic)
		return
	}

	var source, deviceID string
	var eventEnvelopes []*envelope.EventEnvelope

	if mt, ok := transformer.(MultiTransformer); ok {
		source, deviceID, eventEnvelopes = mt.TransformMulti(mqttTopic, payload)
	} else {
		var env *envelope.EventEnvelope
		source, deviceID, env = transformer.Transform(mqttTopic, payload)
		if env != nil {
			eventEnvelopes = append(eventEnvelopes, env)
		}
	}

	if len(eventEnvelopes) == 0 {
		return
	}

	areaSlug := "global"
	// HA statestream sensors are read-only and named by free-form HA object_ids:
	// they must not steer action routing for a same-named target, nor land in
	// an area because their name happens to start with a shortcode.
	if source != haStatestreamSource {
		s.mu.Lock()
		s.sourceCache[deviceID] = source
		s.mu.Unlock()

		// Resolve Area via iot-utils-go (ADR 009)
		if area, ok := areas.GetByEntityID(deviceID); ok {
			areaSlug = area.Slug
		}
	}

	for _, eventEnvelope := range eventEnvelopes {
		if eventEnvelope == nil {
			continue
		}

		domain := eventDomain(eventEnvelope)
		key, val, hasState := stateKV(domain, eventEnvelope)

		idPrefix := "evt"
		if retained {
			if !hasState || isMomentary(mqttTopic, eventEnvelope) {
				slog.Debug("Ignoring retained non-state payload", "topic", mqttTopic, "domain", domain)
				continue
			}
			if !s.resyncNeedsEvent(key, val) {
				continue
			}
			idPrefix = "resync"
		}

		// Wrap metadata
		eventEnvelope.Id = fmt.Sprintf("%s_%d", idPrefix, time.Now().UnixNano())
		eventEnvelope.Source = source
		eventEnvelope.Topic = mqttTopic
		eventEnvelope.Timestamp = timestamppb.Now()

		// Construct NATS Subject (ADR 010)
		natsSubject := fmt.Sprintf("iot.v1.events.%s.%s.%s.%s", source, areaSlug, domain, deviceID)
		natsSubject = strings.ReplaceAll(natsSubject, "/", ".")

		data, err := proto.Marshal(eventEnvelope)
		if err != nil {
			slog.Error("Failed to marshal event", "error", err)
			continue
		}

		// KV first: consumers evaluate their conditions against the KV when
		// the event arrives.
		if hasState {
			s.putState(key, val)
		}

		slog.Debug("Publishing NATS event", "subject", natsSubject, "area", areaSlug, "retained", retained)
		if err := s.nats.Publish(natsSubject, data); err != nil {
			slog.Error("Failed to publish to NATS", "subject", natsSubject, "error", err)
		}
	}
}

// resyncNeedsEvent reports whether a retained state differs from the KV, i.e.
// the change was missed while disconnected. On a lookup error the state is
// skipped entirely: publishing could flood consumers with known states, and a
// blind write would stall the inbox worker on a struggling NATS. The next
// reconnect resyncs it.
func (s *Service) resyncNeedsEvent(key, val string) bool {
	cur, found, err := s.nats.GetKV(key)
	switch {
	case err != nil:
		slog.Warn("Failed to read KV state for resync, skipping", "key", key, "error", err)
		s.resyncFailed++
		return false
	case found && string(cur) == val:
		s.resyncUnchanged++
		return false
	default:
		slog.Info("Retained state differs from KV, publishing missed change", "key", key, "kv", string(cur), "retained", val)
		s.resyncChanged++
		return true
	}
}

func (s *Service) putState(key, val string) {
	if err := s.nats.PutKV(key, []byte(val)); err != nil {
		slog.Error("Failed to update KV state", "key", key, "error", err)
	} else {
		slog.Debug("Updated KV state", "key", key, "value", val)
	}
}

func eventDomain(env *envelope.EventEnvelope) string {
	switch env.Payload.(type) {
	case *envelope.EventEnvelope_BinarySensor:
		return "binary_sensor"
	case *envelope.EventEnvelope_Light:
		return "light"
	case *envelope.EventEnvelope_Sensor:
		return "sensor"
	}
	return "unknown"
}

// stateKV returns the State Store (KV) entry an event maps to.
func stateKV(domain string, env *envelope.EventEnvelope) (key, val string, ok bool) {
	onOff := func(state common.BinaryState) string {
		if state == common.BinaryState_BINARY_STATE_ON {
			return "ON"
		}
		return "OFF"
	}
	switch domain {
	case "sensor":
		if e := env.GetSensor(); e != nil {
			return domain + "." + e.Id, e.Value, true
		}
	case "binary_sensor":
		if e := env.GetBinarySensor(); e != nil {
			return domain + "." + e.EntityId, onOff(e.State), true
		}
	case "light":
		if e := env.GetLight(); e != nil {
			return domain + "." + e.EntityId, onOff(e.State), true
		}
	}
	return "", "", false
}

// isMomentary reports whether env records an occurrence rather than a state.
// A retained copy of it is an old button press and must never be replayed.
func isMomentary(topic string, env *envelope.EventEnvelope) bool {
	// Z2M carries button presses in the "action" field, which the transformer
	// maps to a doorbell event; with retain enabled the last press stays on the
	// device's state topic until its next report.
	if bs := env.GetBinarySensor(); bs != nil && bs.DeviceClass == "doorbell" {
		return true
	}
	// Z-Wave "On"/"Off" are write-only level-change buttons, not the switch
	// state (that is currentValue), but zwave-js-ui retains them too.
	if strings.HasPrefix(topic, "zwave/") {
		switch path.Base(topic) {
		case "On", "Off":
			return true
		}
	}
	return false
}

func (s *Service) handleAction(msg *natsgo.Msg) {
	// A panic in a nats.go callback would otherwise crash the process and
	// silently drop the command.
	defer recoverPanic("nats action", msg.Subject)

	slog.Debug("Received message on NATS action topic", "subject", msg.Subject)
	var req action.ActionRequest
	if err := proto.Unmarshal(msg.Data, &req); err != nil {
		slog.Error("Failed to unmarshal ActionRequest", "error", err)
		return
	}

	slog.Info("Received NATS action", "id", req.Id, "target", req.TargetEntity)

	// 1. Handle Scene Actions (Priority)
	if sceneCmd := req.GetScene(); sceneCmd != nil {
		// a. Home Assistant Scene Activation
		haTopic := "homeassistant/scene/activate"
		haPayload, _ := json.Marshal(map[string]interface{}{
			"scene_id": sceneCmd.SceneId,
		})
		slog.Info("Activating Home Assistant Scene", "scene", sceneCmd.SceneId)
		s.mqtt.Publish(haTopic, haPayload)

		// b. Zigbee Group Scene Recall
		if req.TargetEntity != "" {
			z2mTopic := fmt.Sprintf("zigbee/%s/set", req.TargetEntity)

			// Try to parse sceneId as int or lookup from cache
			sceneIDInt := -1
			if id, err := strconv.Atoi(sceneCmd.SceneId); err == nil {
				sceneIDInt = id
			} else if id, ok := s.z2mSceneID(req.TargetEntity, sceneCmd.SceneId); ok {
				sceneIDInt = id
			}

			var z2mPayload []byte
			if sceneIDInt != -1 {
				z2mPayload, _ = json.Marshal(map[string]interface{}{
					"scene_recall": sceneIDInt,
				})
				slog.Info("Recalling Zigbee Scene by ID", "group", req.TargetEntity, "sceneName", sceneCmd.SceneId, "sceneId", sceneIDInt)
			} else {
				z2mPayload, _ = json.Marshal(map[string]interface{}{
					"scene_recall": sceneCmd.SceneId,
				})
				slog.Info("Recalling Zigbee Scene by String (Fallback)", "group", req.TargetEntity, "scene", sceneCmd.SceneId)
			}

			s.mqtt.Publish(z2mTopic, z2mPayload)
		}
		return
	}

	// Handle Notification Actions
	if notifCmd := req.GetNotification(); notifCmd != nil {
		slog.Info("Action: Received notification request", "target", req.TargetEntity, "title", notifCmd.Title)

		// HA Notify Service Payload
		haTopic := "homeassistant/notify"
		if strings.HasPrefix(req.TargetEntity, "notify.") {
			// e.g. "notify.mobile_app_dennis" -> "homeassistant/notify/mobile_app_dennis"
			haTopic = "homeassistant/" + strings.ReplaceAll(req.TargetEntity, ".", "/")
		}

		haPayload, _ := json.Marshal(map[string]interface{}{
			"title":   notifCmd.Title,
			"message": notifCmd.Message,
			"data":    notifCmd.Data,
		})

		slog.Info("Action: Publishing to HA MQTT topic", "topic", haTopic, "payload", string(haPayload))
		if err := s.mqtt.Publish(haTopic, haPayload); err != nil {
			slog.Error("Action: Failed to publish notification to MQTT", "topic", haTopic, "error", err)
		}
		return
	}

	// 4. Handle Light Actions
	if lightCmd := req.GetLight(); lightCmd != nil {
		state := "OFF"
		zwaveVal := 0
		if lightCmd.State == common.BinaryState_BINARY_STATE_ON {
			state = "ON"
			zwaveVal = 255 // Binary default
			if lightCmd.Brightness > 0 {
				zwaveVal = int(lightCmd.Brightness * 99.0) // Multilevel default
			} else {
				zwaveVal = 99
			}
		}

		// Zigbee Payload
		payload := map[string]interface{}{
			"state": state,
		}
		if lightCmd.Brightness > 0 {
			payload["brightness"] = int(lightCmd.Brightness * 255.0)
		}

		data, _ := json.Marshal(payload)

		// Z-Wave Payload (TargetValue wrapper)
		zwavePayload, _ := json.Marshal(map[string]interface{}{"value": zwaveVal})

		// Resolve Source (Cache with Broadcast Fallback)
		sources := []string{"zigbee", "zwave", "ccu2"} // List of known sources
		if cachedSource, ok := s.cachedSource(req.TargetEntity); ok {
			sources = []string{cachedSource}
			slog.Debug("Source cache hit", "device", req.TargetEntity, "source", cachedSource)
		} else {
			slog.Info("Source cache miss, broadcasting action to all sources", "device", req.TargetEntity)
		}

		for _, src := range sources {
			topic := fmt.Sprintf("%s/%s/set", src, req.TargetEntity)
			var finalPayload []byte
			if src == "zigbee" || src == "ccu2" {
				finalPayload = data
			} else {
				finalPayload = zwavePayload
			}

			// Handle Z-Wave specific topic pattern if using Z-Wave JS UI "ValueID" style
			if src == "zwave" {
				// 1. Try Multilevel (Endpoint 1 is standard for many Z-Wave switches)
				mlTopic := fmt.Sprintf("zwave/%s/switch_multilevel/endpoint_1/targetValue/set", req.TargetEntity)
				slog.Info("Executing Z-Wave Multilevel Action", "topic", mlTopic, "payload", string(zwavePayload))
				s.mqtt.Publish(mlTopic, []byte(zwavePayload))

				// Use the simple /set topic as general fallback for Z-Wave
				finalPayload = []byte(zwavePayload)
			}

			slog.Info("Executing Light Action via MQTT", "topic", topic, "source", src)
			if err := s.mqtt.Publish(topic, finalPayload); err != nil {
				slog.Error("Failed to publish MQTT action", "topic", topic, "error", err)
			}
		}
	}

	// 5. Handle Cover Actions
	if coverCmd := req.GetCover(); coverCmd != nil {
		slog.Info("Action: Received cover request", "target", req.TargetEntity, "state", coverCmd.State)

		state := "STOP"
		if coverCmd.State == "OPEN" {
			state = "OPEN"
		} else if coverCmd.State == "CLOSE" {
			state = "CLOSE"
		}

		payload := map[string]interface{}{
			"state": state,
		}
		if coverCmd.Position != nil {
			payload["position"] = *coverCmd.Position
		}

		data, _ := json.Marshal(payload)

		// Resolve Source
		sources := []string{"zigbee", "zwave", "ccu2"}
		if cachedSource, ok := s.cachedSource(req.TargetEntity); ok {
			sources = []string{cachedSource}
		}

		for _, src := range sources {
			var topic string
			if src == "ccu2" {
				topic = fmt.Sprintf("%s/cover/%s/set", src, req.TargetEntity)
			} else {
				topic = fmt.Sprintf("%s/%s/set", src, req.TargetEntity)
			}

			var finalPayload []byte
			if src == "ccu2" {
				if coverCmd.Position != nil {
					finalPayload = []byte(fmt.Sprintf("%d", *coverCmd.Position))
				} else {
					finalPayload = []byte(state)
				}
			} else {
				finalPayload = data
			}

			slog.Info("Executing Cover Action via MQTT", "topic", topic, "source", src, "payload", string(finalPayload))
			if err := s.mqtt.Publish(topic, finalPayload); err != nil {
				slog.Error("Failed to publish MQTT action for cover", "topic", topic, "error", err)
			}
		}
	}
}

// isCommandTopic reports whether topic is a command sent to a device (for
// example our own ccu2/cover/<id>/set), not a state report.
func isCommandTopic(topic string) bool {
	return strings.HasSuffix(topic, "/set") || strings.Contains(topic, "/set/")
}

func recoverPanic(kind, subject string) {
	if r := recover(); r != nil {
		slog.Error("Recovered from panic in handler", "kind", kind, "subject", subject, "panic", r, "stack", string(debug.Stack()))
	}
}

func (s *Service) cachedSource(deviceID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.sourceCache[deviceID]
	return src, ok
}

func (s *Service) z2mSceneID(group, name string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.z2mScenes[group][name]
	return id, ok
}
