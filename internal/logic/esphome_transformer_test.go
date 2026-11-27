package logic

import (
	"slices"
	"testing"
)

// Each sensor component gets its own entity and subject, so a rule on one
// component is not triggered by the device's other sensors.
func TestServiceKeysESPHomeSensorsByComponent(t *testing.T) {
	svc, n := newTestService()
	svc.handleMQTTMessage("office_presence/sensor/illuminance/state", []byte("2.5"), false)
	svc.handleMQTTMessage("office_presence/sensor/temperature/state", []byte("21.2"), false)

	want := []string{
		"iot.v1.events.esphome.global.sensor.office_presence_illuminance",
		"iot.v1.events.esphome.global.sensor.office_presence_temperature",
	}
	if !slices.Equal(n.subjects, want) {
		t.Fatalf("published %v, want %v", n.subjects, want)
	}
	if got := decodeEvent(t, n.events[0]).GetSensor().GetEntityId(); got != "office_presence_illuminance" {
		t.Errorf("event EntityId = %q, want office_presence_illuminance", got)
	}
	if n.kv["sensor.office_presence_illuminance"] != "2.5" || n.kv["sensor.office_presence_temperature"] != "21.2" {
		t.Errorf("KV = %v", n.kv)
	}
}

// Presence rules trigger on the bare device, so occupancy keeps it.
func TestServiceKeepsESPHomeOccupancyOnDevice(t *testing.T) {
	svc, n := newTestService()
	svc.handleMQTTMessage("office_presence/binary_sensor/occupancy/state", []byte("ON"), false)
	svc.handleMQTTMessage("office_presence/binary_sensor/mmwave/state", []byte("ON"), false)

	want := []string{
		"iot.v1.events.esphome.global.binary_sensor.office_presence",
		"iot.v1.events.esphome.global.binary_sensor.office_presence_mmwave",
	}
	if !slices.Equal(n.subjects, want) {
		t.Fatalf("published %v, want %v", n.subjects, want)
	}
	if got := decodeEvent(t, n.events[0]).GetBinarySensor().GetEntityId(); got != "office_presence" {
		t.Errorf("event EntityId = %q, want office_presence", got)
	}
	if n.kv["binary_sensor.office_presence"] != "ON" || n.kv["binary_sensor.office_presence_mmwave"] != "ON" {
		t.Errorf("KV = %v", n.kv)
	}
}
