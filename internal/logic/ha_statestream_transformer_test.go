package logic

import (
	"slices"
	"testing"
)

func TestHAStatestreamTransformer(t *testing.T) {
	transformer := &HAStatestreamTransformer{}

	for topic, want := range map[string]bool{
		"homeassistant/sensor/sun_solar_elevation/state":      true,
		"homeassistant/sensor/sun_solar_elevation/azimuth":    false, // attribute topic
		"homeassistant/binary_sensor/some_door/state":         false,
		"homeassistant/sensor/node/sun_solar_elevation/state": false,
		"homeassistant/notify/mobile_app_dennis":              false,
		"esphome_device/sensor/illuminance/state":             false,
	} {
		if got := transformer.Accepts(topic); got != want {
			t.Errorf("Accepts(%q) = %v, want %v", topic, got, want)
		}
	}

	tests := []struct {
		name        string
		payload     string
		wantNumeric float64
	}{
		{"negative float", "-3.45", -3.45},
		{"integer", "12", 12},
		{"unavailable", "unavailable", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, devID, env := transformer.Transform("homeassistant/sensor/sun_solar_elevation/state", []byte(tt.payload))
			if src != "homeassistant" {
				t.Errorf("got source %q", src)
			}
			if devID != "sun_solar_elevation" {
				t.Errorf("got deviceID %q", devID)
			}
			if env == nil {
				t.Fatal("expected envelope")
			}
			s := env.GetSensor()
			if s == nil {
				t.Fatalf("expected Sensor payload, got %T", env.Payload)
			}
			if s.Id != "sun_solar_elevation" || s.EntityId != "sun_solar_elevation" {
				t.Errorf("got Id %q / EntityId %q", s.Id, s.EntityId)
			}
			if s.Source != "homeassistant" {
				t.Errorf("got sensor source %q", s.Source)
			}
			if s.Value != tt.payload {
				t.Errorf("got value %q, want %q", s.Value, tt.payload)
			}
			if s.NumericValue != tt.wantNumeric {
				t.Errorf("got numeric value %v, want %v", s.NumericValue, tt.wantNumeric)
			}
		})
	}
}

// The statestream topic has the same shape as an ESPHome state topic; the
// service must route it to the HA transformer so the KV key matches the HA
// entity_id that automation conditions reference.
func TestServiceRoutesHAStatestreamBeforeESPHome(t *testing.T) {
	svc, n := newTestService()
	svc.handleMQTTMessage("homeassistant/sensor/sun_solar_elevation/state", []byte("-3.45"), false)

	if want := []string{"iot.v1.events.homeassistant.global.sensor.sun_solar_elevation"}; !slices.Equal(n.subjects, want) {
		t.Errorf("published %v, want %v", n.subjects, want)
	}
	if got := n.kv["sensor.sun_solar_elevation"]; got != "-3.45" {
		t.Errorf("KV = %v, want sensor.sun_solar_elevation=-3.45", n.kv)
	}
	if env := decodeEvent(t, n.events[0]); env.GetSensor().GetEntityId() != "sun_solar_elevation" {
		t.Errorf("event EntityId = %q", env.GetSensor().GetEntityId())
	}
}

// Statestream retains the state topic, so a reconnect must resync the KV
// without republishing an unchanged value.
func TestRetainedHAStatestreamResync(t *testing.T) {
	const topic = "homeassistant/sensor/sun_solar_elevation/state"
	svc, n := newTestService()
	n.kv["sensor.sun_solar_elevation"] = "-3.45"

	svc.handleMQTTMessage(topic, []byte("-3.45"), true)
	if len(n.subjects) != 0 {
		t.Errorf("unchanged retained state published %v", n.subjects)
	}

	svc.handleMQTTMessage(topic, []byte("-1.2"), true)
	if len(n.subjects) != 1 || n.kv["sensor.sun_solar_elevation"] != "-1.2" {
		t.Errorf("changed retained state: subjects %v, KV %v", n.subjects, n.kv)
	}
}
