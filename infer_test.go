package main

import (
	"strings"
	"testing"
)

func TestInferTelegrafFormat(t *testing.T) {
	payload := []byte(`{"fields":{"battery_charge_percent":100,"battery_voltage":27.3,"ups_status":"OL","nested":{"a":1}},"tags":{"model":"Back-UPS NS 1500M2","serial":"5B2110T56265","ups_name":"ups"},"name":"upsd","timestamp":1786147250}`)

	inf, err := inferDevice(payload)
	if err != nil {
		t.Fatalf("inferDevice: %v", err)
	}
	if inf.Model != "Back-UPS NS 1500M2" {
		t.Errorf("model = %q", inf.Model)
	}
	if inf.Serial != "5B2110T56265" {
		t.Errorf("serial = %q", inf.Serial)
	}
	if inf.Name != "ups" {
		t.Errorf("name = %q, want ups (from ups_name tag)", inf.Name)
	}
	if len(inf.Fields) != 3 {
		t.Errorf("fields = %d, want 3 (nested object excluded)", len(inf.Fields))
	}
	if v, ok := inf.Fields["battery_charge_percent"].(float64); !ok || v != 100 {
		t.Errorf("battery_charge_percent = %v, want 100", inf.Fields["battery_charge_percent"])
	}
}

func TestInferFlatFormat(t *testing.T) {
	payload := []byte(`{"temperature":31.5,"load_percent":18,"status":"OK","timestamp":1786147250}`)

	inf, err := inferDevice(payload)
	if err != nil {
		t.Fatalf("inferDevice: %v", err)
	}
	if len(inf.Fields) != 3 {
		t.Errorf("fields = %d, want 3 (timestamp excluded)", len(inf.Fields))
	}
	if _, ok := inf.Fields["timestamp"]; ok {
		t.Error("timestamp should be excluded")
	}
}

func TestInferNonJSON(t *testing.T) {
	if _, err := inferDevice([]byte("not json")); err == nil {
		t.Error("expected error for non-JSON payload")
	}
}

func TestGuessEntityNumeric(t *testing.T) {
	cases := []struct {
		field string
		dc    string
		unit  string
	}{
		{"load_percent", "", "%"},                    // load is plain percentage
		{"battery_charge_percent", "battery", "%"},   // charge/percent -> battery
		{"battery_voltage", "voltage", "V"},          // voltage
		{"battery_runtime_low", "duration", "s"},     // runtime -> duration
		{"power_draw", "power", "W"},                 // power
		{"nominal_power", "", ""},                    // nominal power excluded from device_class
		{"temperature_gpu", "temperature", "°C"},     // temp
		{"input_frequency", "frequency", "Hz"},       // frequency
		{"ups_status", "", ""},                       // string status: no class
	}
	for _, c := range cases {
		e := guessEntity(c.field, 1.0)
		if e.DeviceClass != c.dc || e.Unit != c.unit {
			t.Errorf("guessEntity(%q) = dc=%q unit=%q, want dc=%q unit=%q",
				c.field, e.DeviceClass, e.Unit, c.dc, c.unit)
		}
	}
}

func TestGuessEntityStringStatus(t *testing.T) {
	e := guessEntity("ups_status", "OL")
	if e.DeviceClass != "" {
		t.Errorf("status device_class = %q, want empty", e.DeviceClass)
	}
	if e.Icon == "" {
		t.Error("status entity should get an icon")
	}
}

func TestBuildDiscoveryTemplate(t *testing.T) {
	dev := &Device{Topic: "home/ups/ups", Name: "ups", Model: "Back-UPS NS 1500M2"}
	ent := Entity{Field: "battery_charge_percent", Name: "battery_charge_percent", DeviceClass: "battery", Unit: "%"}

	topic, payload, err := BuildDiscovery(dev, ent, dev.Topic)
	if err != nil {
		t.Fatalf("BuildDiscovery: %v", err)
	}
	// topic must be a fresh unique_id (v2 prefix, no stale discovery-hash collisions)
	if !strings.Contains(topic, "mqtt2ha_v2_home_ups_ups_battery_charge_percent") {
		t.Errorf("unexpected discovery topic: %s", topic)
	}
	// value_template must handle Telegraf's nested fields format
	body := string(payload)
	if !strings.Contains(body, `value_json.fields.battery_charge_percent`) {
		t.Errorf("value_template missing fields fallback: %s", body)
	}
	if !strings.Contains(body, `value_json.battery_charge_percent`) {
		t.Errorf("value_template missing flat fallback: %s", body)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"home/ups/ups", "home_ups_ups"},
		{"telegraf/home-ai/nvidia_smi", "telegraf_home-ai_nvidia_smi"},
		{"a b.c", "a_b_c"},
	}
	for _, c := range cases {
		if got := sanitize(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
