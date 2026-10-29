package logic

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// z2mLoggingTopic carries zigbee2mqtt's log lines from level info upwards as
// {"message":"z2m: ...","level":"error"}. The Bridge extension publishes it
// unconditionally, independent of advanced.log_output.
const z2mLoggingTopic = "zigbee/bridge/logging"

// transientZigbeeErrors are send failures where the coordinator found no free
// radio channel. They usually clear within milliseconds, so resending the same
// command is worthwhile; any other error is left alone.
var transientZigbeeErrors = []string{
	"MacNoChannelAccess",
	"MacChannelAccessFailure",
}

// z2mPublishFailedRe matches the error z2m logs when a converter fails, e.g.
// Publish 'set' 'scene_recall' to 'kt_lights' failed: 'Error: Command 6 genScenes.recall(...) failed (...)'
var z2mPublishFailedRe = regexp.MustCompile(`(?s)Publish '([^']+)' '([^']+)' to '([^']+)' failed: '(.*)'`)

type z2mPublishFailure struct {
	Key    string
	Target string
	Error  string
}

func parseZ2MPublishFailure(payload []byte) (z2mPublishFailure, bool) {
	var entry struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &entry); err != nil || entry.Level != "error" {
		return z2mPublishFailure{}, false
	}
	m := z2mPublishFailedRe.FindStringSubmatch(entry.Message)
	if m == nil {
		return z2mPublishFailure{}, false
	}
	return z2mPublishFailure{Key: m[2], Target: m[3], Error: m[4]}, true
}

func (f z2mPublishFailure) transient() bool {
	for _, e := range transientZigbeeErrors {
		if strings.Contains(f.Error, e) {
			return true
		}
	}
	return false
}

type SceneRetryConfig struct {
	// Delays[i] is the wait between the failure report and retry i+1; the
	// last entry applies to all further retries.
	Delays     []time.Duration
	MaxRetries int
	// Window is how long after the original recall a failure still triggers
	// a retry, so a late report never replays a stale scene.
	Window time.Duration
}

func DefaultSceneRetryConfig() SceneRetryConfig {
	return SceneRetryConfig{
		Delays:     []time.Duration{200 * time.Millisecond, 500 * time.Millisecond},
		MaxRetries: 2,
		Window:     5 * time.Second,
	}
}

func (c SceneRetryConfig) enabled() bool {
	return c.MaxRetries > 0 && len(c.Delays) > 0 && c.Window > 0
}

func (c SceneRetryConfig) delay(retry int) time.Duration {
	return c.Delays[min(retry, len(c.Delays)-1)]
}

type stopper interface{ Stop() bool }

type sceneRecall struct {
	topic   string
	payload []byte
	sentAt  time.Time
	retries int
	timer   stopper // non-nil while a retry is scheduled but not yet sent
}

// sceneRetrier resends a Zigbee scene recall when z2m reports that sending it
// failed with a transient radio error. Recalls are remembered from NATS action
// callbacks and failures arrive via the MQTT inbox worker, so all state is
// guarded by mu.
type sceneRetrier struct {
	cfg       SceneRetryConfig
	publish   func(topic string, payload []byte) error
	now       func() time.Time
	afterFunc func(time.Duration, func()) stopper

	mu      sync.Mutex
	recalls map[string]*sceneRecall // z2m group -> last recall sent to it
	closed  bool
}

func newSceneRetrier(cfg SceneRetryConfig, publish func(topic string, payload []byte) error) *sceneRetrier {
	return &sceneRetrier{
		cfg:       cfg,
		publish:   publish,
		now:       time.Now,
		afterFunc: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
		recalls:   make(map[string]*sceneRecall),
	}
}

// remember records a recall right before it is published. A newer recall for
// the same group supersedes a pending retry of the older one.
func (r *sceneRetrier) remember(group, topic string, payload []byte) {
	if !r.cfg.enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.recalls[group]; old != nil && old.timer != nil {
		old.timer.Stop()
	}
	r.recalls[group] = &sceneRecall{topic: topic, payload: payload, sentAt: r.now()}
}

// handleLog evaluates a zigbee/bridge/logging message. It only schedules the
// retry, so the MQTT inbox worker is never blocked.
func (r *sceneRetrier) handleLog(payload []byte) {
	f, ok := parseZ2MPublishFailure(payload)
	if !ok || f.Key != "scene_recall" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.recalls[f.Target]
	if r.closed || rec == nil {
		slog.Debug("Ignoring Zigbee scene recall failure without a recall of ours", "group", f.Target)
		return
	}
	if rec.timer != nil {
		slog.Debug("Ignoring Zigbee scene recall failure, retry already scheduled", "group", f.Target)
		return
	}

	switch {
	case !f.transient():
		slog.Warn("Zigbee scene recall failed with a non-transient error, not retrying", "group", f.Target, "payload", string(rec.payload), "error", f.Error)
	case r.now().Sub(rec.sentAt) > r.cfg.Window:
		slog.Warn("Giving up Zigbee scene recall, failure reported after the retry window", "group", f.Target, "payload", string(rec.payload), "window", r.cfg.Window, "error", f.Error)
	case rec.retries >= r.cfg.MaxRetries:
		slog.Warn("Giving up Zigbee scene recall, retries exhausted", "group", f.Target, "payload", string(rec.payload), "retries", rec.retries, "error", f.Error)
	default:
		d := r.cfg.delay(rec.retries)
		rec.retries++
		slog.Info("Scheduling Zigbee scene recall retry", "group", f.Target, "payload", string(rec.payload), "attempt", rec.retries, "max", r.cfg.MaxRetries, "delay", d, "error", f.Error)
		rec.timer = r.afterFunc(d, func() { r.retry(f.Target, rec) })
		return
	}
	delete(r.recalls, f.Target)
}

func (r *sceneRetrier) retry(group string, rec *sceneRecall) {
	r.mu.Lock()
	if r.closed || r.recalls[group] != rec {
		r.mu.Unlock()
		return
	}
	rec.timer = nil
	if r.now().Sub(rec.sentAt) > r.cfg.Window {
		delete(r.recalls, group)
		r.mu.Unlock()
		slog.Warn("Giving up Zigbee scene recall, retry window elapsed", "group", group, "payload", string(rec.payload), "window", r.cfg.Window)
		return
	}
	attempt := rec.retries
	r.mu.Unlock()

	slog.Info("Retrying Zigbee scene recall", "group", group, "payload", string(rec.payload), "attempt", attempt, "max", r.cfg.MaxRetries)
	if err := r.publish(rec.topic, rec.payload); err != nil {
		slog.Error("Failed to publish Zigbee scene recall retry", "topic", rec.topic, "error", err)
	}
}

// close cancels pending retries; it runs before MQTT disconnects.
func (r *sceneRetrier) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, rec := range r.recalls {
		if rec.timer != nil {
			rec.timer.Stop()
		}
	}
}

// topicMatches reports whether an MQTT topic filter (with + and # wildcards)
// matches topic.
func topicMatches(filter, topic string) bool {
	f := strings.Split(filter, "/")
	t := strings.Split(topic, "/")
	for i, part := range f {
		if part == "#" {
			return true
		}
		if i >= len(t) || (part != "+" && part != t[i]) {
			return false
		}
	}
	return len(f) == len(t)
}
