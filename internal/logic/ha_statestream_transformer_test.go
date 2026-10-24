package logic

import (
	"slices"
	"testing"
)

const sunElevationTopic = "homeassistant/sensor/sun_solar_elevation/state"

func TestHAStatestreamTransformer(t *testing.T) {
	transformer := &HAStatestreamTransformer{}

	for topic, want := range map[string]bool{
		sunElevationTopic: true,
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

	for payload, want := range map[string]float64{"-3.45": -3.45, "12": 12, "0": 0} {
		src, devID, env := transformer.Transform(sunElevationTopic, []byte(payload))
		if src != "homeassistant" || devID != "sun_solar_elevation" {
			t.Errorf("%s: got source %q, deviceID %q", payload, src, devID)
		}
		s := env.GetSensor()
		if s == nil {
			t.Fatalf("%s: expected Sensor payload, got %v", payload, env)
		}
		if s.Id != "sun_solar_elevation" || s.EntityId != "sun_solar_elevation" || s.Source != "homeassistant" {
			t.Errorf("%s: got Id %q / EntityId %q / Source %q", payload, s.Id, s.EntityId, s.Source)
		}
		if s.Value != payload || s.NumericValue != want || s.Unit != "°" {
			t.Errorf("%s: got value %q, numeric %v, unit %q", payload, s.Value, s.NumericValue, s.Unit)
		}
	}

	for _, payload := range []string{"unavailable", "unknown", "", "NaN", "Inf"} {
		if _, _, env := transformer.Transform(sunElevationTopic, []byte(payload)); env != nil {
			t.Errorf("non-numeric payload %q produced %v", payload, env)
		}
	}
}

// HA reports unknown/unavailable while restarting and statestream retains it;
// the last valid value must survive in the KV and no event may fire.
func TestHAStatestreamNonNumericKeepsLastValue(t *testing.T) {
	for _, retained := range []bool{false, true} {
		svc, n := newTestService()
		n.kv["sensor.sun_solar_elevation"] = "-3.45"
		for _, payload := range []string{"unavailable", "unknown", ""} {
			svc.handleMQTTMessage(sunElevationTopic, []byte(payload), retained)
		}
		if len(n.subjects) != 0 || n.kv["sensor.sun_solar_elevation"] != "-3.45" {
			t.Errorf("retained=%v: published %v, KV %v", retained, n.subjects, n.kv)
		}
	}
}

func TestESPHomeTransformerIgnoresHAStatestream(t *testing.T) {
	esphome := &ESPHomeTransformer{}
	for _, topic := range []string{sunElevationTopic, "homeassistant/binary_sensor/some_door/state"} {
		if esphome.Accepts(topic) {
			t.Errorf("ESPHome accepts HA statestream topic %q", topic)
		}
	}
	if !esphome.Accepts("esphome_device/sensor/illuminance/state") {
		t.Error("ESPHome no longer accepts its own topics")
	}
}

func TestServiceHandlesHAStatestream(t *testing.T) {
	svc, n := newTestService()
	svc.handleMQTTMessage(sunElevationTopic, []byte("-3.45"), false)

	if want := []string{"iot.v1.events.homeassistant.global.sensor.sun_solar_elevation"}; !slices.Equal(n.subjects, want) {
		t.Fatalf("published %v, want %v", n.subjects, want)
	}
	if got := n.kv["sensor.sun_solar_elevation"]; got != "-3.45" {
		t.Errorf("KV = %v, want sensor.sun_solar_elevation=-3.45", n.kv)
	}
	if env := decodeEvent(t, n.events[0]); env.GetSensor().GetEntityId() != "sun_solar_elevation" {
		t.Errorf("event EntityId = %q", env.GetSensor().GetEntityId())
	}
}

// HA object_ids are free-form: a name starting with an area shortcode ("be")
// must stay global, and a read-only sensor must not capture action routing for
// a same-named target.
func TestServiceKeepsHAStatestreamOutOfAreasAndActionRouting(t *testing.T) {
	svc, n := newTestService()
	svc.handleMQTTMessage("homeassistant/sensor/be_light_1/state", []byte("21.5"), false)

	if want := []string{"iot.v1.events.homeassistant.global.sensor.be_light_1"}; !slices.Equal(n.subjects, want) {
		t.Errorf("published %v, want %v", n.subjects, want)
	}
	if src, ok := svc.cachedSource("be_light_1"); ok {
		t.Errorf("sourceCache[be_light_1] = %q, want no entry", src)
	}
}

// Statestream retains the state topic, so a reconnect must resync the KV
// without republishing an unchanged value.
func TestRetainedHAStatestreamResync(t *testing.T) {
	svc, n := newTestService()
	n.kv["sensor.sun_solar_elevation"] = "-3.45"

	svc.handleMQTTMessage(sunElevationTopic, []byte("-3.45"), true)
	if len(n.subjects) != 0 {
		t.Errorf("unchanged retained state published %v", n.subjects)
	}

	svc.handleMQTTMessage(sunElevationTopic, []byte("-1.2"), true)
	if len(n.subjects) != 1 || n.kv["sensor.sun_solar_elevation"] != "-1.2" {
		t.Errorf("changed retained state: subjects %v, KV %v", n.subjects, n.kv)
	}
}
