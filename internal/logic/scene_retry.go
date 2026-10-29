package logic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
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

// z2mSceneIDRe extracts the scene from zigbee-herdsman's failed command, e.g.
// genScenes.recall({"groupid":6,"sceneid":2}).
var z2mSceneIDRe = regexp.MustCompile(`"sceneid":\s*(\d+)`)

type z2mPublishFailure struct {
	Key    string
	Target string
	Error  string
}

// z2mErrorMessage returns the message of an error-level z2m log line.
func z2mErrorMessage(payload []byte) (string, bool) {
	var entry struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &entry); err != nil || entry.Level != "error" {
		return "", false
	}
	return entry.Message, true
}

func parseZ2MPublishFailure(message string) (z2mPublishFailure, bool) {
	m := z2mPublishFailedRe.FindStringSubmatch(message)
	if m == nil {
		return z2mPublishFailure{}, false
	}
	return z2mPublishFailure{Key: m[2], Target: m[3], Error: m[4]}, true
}

func (f z2mPublishFailure) sceneID() (int, bool) {
	m := z2mSceneIDRe.FindStringSubmatch(f.Error)
	if m == nil {
		return 0, false
	}
	id, err := strconv.Atoi(m[1])
	return id, err == nil
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

// Validate rejects negative settings and a window that ends before the first
// retry could go out; zero MaxRetries, Window or no Delays disable the retry.
func (c SceneRetryConfig) Validate() error {
	for _, d := range c.Delays {
		if d < 0 {
			return fmt.Errorf("scene retry delay %v is negative", d)
		}
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("scene retry max %d is negative", c.MaxRetries)
	}
	if c.Window < 0 {
		return fmt.Errorf("scene retry window %v is negative", c.Window)
	}
	if c.enabled() && c.Delays[0] >= c.Window {
		return fmt.Errorf("scene retry window %v must exceed the first delay %v", c.Window, c.Delays[0])
	}
	return nil
}

func (c SceneRetryConfig) enabled() bool {
	return c.MaxRetries > 0 && len(c.Delays) > 0 && c.Window > 0
}

func (c SceneRetryConfig) delay(retry int) time.Duration {
	return c.Delays[min(retry, len(c.Delays)-1)]
}

type stopper interface{ Stop() bool }

// noSceneID marks a recall by scene name, whose numeric ID is unknown.
const noSceneID = -1

type sceneRecall struct {
	topic    string
	payload  []byte
	sceneID  int       // noSceneID for a recall by name
	sentAt   time.Time // original recall; bounds the retries
	lastSent time.Time // original recall or latest retry
	retries  int
	timer    stopper // non-nil while a retry is scheduled but not yet sent
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

	// Counts retries past the closed check, so close can wait for their
	// publish before MQTT disconnects.
	inflight sync.WaitGroup
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
func (r *sceneRetrier) remember(group, topic string, payload []byte, sceneID int) {
	if !r.cfg.enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.recalls[group]; old != nil && old.timer != nil {
		old.timer.Stop()
	}
	now := r.now()
	r.recalls[group] = &sceneRecall{topic: topic, payload: payload, sceneID: sceneID, sentAt: now, lastSent: now}
}

// forget drops the recall of a group when the connector sends it another
// command, so a pending retry cannot undo that newer command.
func (r *sceneRetrier) forget(group string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.recalls[group]
	if rec == nil {
		return
	}
	if rec.timer != nil {
		rec.timer.Stop()
		slog.Info("Cancelling Zigbee scene recall retry, superseded by a newer command", "group", group, "payload", string(rec.payload))
	}
	delete(r.recalls, group)
}

// handleLog evaluates a zigbee/bridge/logging message. It only schedules the
// retry, so the MQTT inbox worker is never blocked.
//
// z2m names only the group, and HA or the z2m frontend recall scenes on the
// same groups, so a failure counts as ours only if its scene ID matches the
// one we recalled. Anything else is left alone, including our entry.
func (r *sceneRetrier) handleLog(payload []byte) {
	if !r.cfg.enabled() || !bytes.Contains(payload, []byte("scene_recall")) {
		return
	}
	msg, ok := z2mErrorMessage(payload)
	if !ok {
		return
	}
	f, ok := parseZ2MPublishFailure(msg)
	if !ok {
		// Surfaces a changed z2m log format, which would silently stop retries.
		if strings.Contains(msg, "scene_recall") {
			slog.Warn("Unrecognized z2m scene_recall error, not retrying", "message", truncate(msg, 160))
		}
		return
	}
	if f.Key != "scene_recall" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.recalls[f.Target]
	if r.closed || rec == nil {
		slog.Debug("Ignoring Zigbee scene recall failure without a recall of ours", "group", f.Target)
		return
	}
	if rec.timer == nil && r.now().Sub(rec.lastSent) > r.cfg.Window {
		// Our last send went through; the failure belongs to a later command.
		delete(r.recalls, f.Target)
		slog.Debug("Ignoring Zigbee scene recall failure outside the retry window", "group", f.Target)
		return
	}

	sceneID, hasID := f.sceneID()
	switch {
	case rec.sceneID == noSceneID:
		slog.Info("Not retrying Zigbee scene recall by name, failure cannot be attributed to it", "group", f.Target, "payload", string(rec.payload), "error", f.Error)
		return
	case !hasID:
		slog.Info("Not retrying Zigbee scene recall, failure names no scene ID", "group", f.Target, "payload", string(rec.payload), "error", f.Error)
		return
	case sceneID != rec.sceneID:
		slog.Debug("Ignoring Zigbee scene recall failure of another scene", "group", f.Target, "scene_id", sceneID, "ours", rec.sceneID)
		return
	case rec.timer != nil:
		slog.Debug("Ignoring Zigbee scene recall failure, retry already scheduled", "group", f.Target)
		return
	}

	switch {
	case !f.transient():
		slog.Warn("Zigbee scene recall failed with a non-transient error, not retrying", "group", f.Target, "payload", string(rec.payload), "error", f.Error)
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
	// Runs on a timer goroutine, where a panic would crash the process.
	defer recoverPanic("scene recall retry", group)

	attempt, ok := r.startRetry(group, rec)
	if !ok {
		return
	}
	defer r.inflight.Done()

	slog.Info("Retrying Zigbee scene recall", "group", group, "payload", string(rec.payload), "attempt", attempt, "max", r.cfg.MaxRetries)
	if err := r.publish(rec.topic, rec.payload); err != nil {
		slog.Error("Failed to publish Zigbee scene recall retry", "topic", rec.topic, "error", err)
	}
}

// startRetry checks under the lock whether rec is still due and, if so,
// registers the retry as in flight. The deferred unlock keeps a recovered
// panic from leaving mu locked, which would block every scene action.
func (r *sceneRetrier) startRetry(group string, rec *sceneRecall) (attempt int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.recalls[group] != rec {
		return 0, false
	}
	rec.timer = nil
	now := r.now()
	if now.Sub(rec.sentAt) > r.cfg.Window {
		delete(r.recalls, group)
		slog.Warn("Giving up Zigbee scene recall, retry window elapsed", "group", group, "payload", string(rec.payload), "window", r.cfg.Window)
		return 0, false
	}
	rec.lastSent = now
	r.inflight.Add(1)
	return rec.retries, true
}

// close cancels pending retries and waits for one being published, so none
// publishes after MQTT disconnects.
func (r *sceneRetrier) close(timeout time.Duration) {
	r.mu.Lock()
	r.closed = true
	for _, rec := range r.recalls {
		if rec.timer != nil {
			rec.timer.Stop()
		}
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("Timed out waiting for Zigbee scene recall retry to publish")
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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
