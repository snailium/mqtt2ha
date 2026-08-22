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
		{"load_percent", "", "%"},                  // load is plain percentage
		{"battery_charge_percent", "battery", "%"}, // charge/percent -> battery
		{"battery_voltage", "voltage", "V"},        // voltage
		{"battery_runtime_low", "duration", "s"},   // runtime -> duration
		{"power_draw", "power", "W"},               // power
		{"nominal_power", "", ""},                  // nominal power excluded from device_class
		{"temperature_gpu", "temperature", "°C"},   // temp
		{"input_frequency", "frequency", "Hz"},     // frequency
		{"ups_status", "", ""},                     // string status: no class
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

	topic, payload, err := BuildDiscovery("homeassistant", dev, ent, dev.Topic)
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

// ---- M3: binary_sensor + extended inference ----

func TestGuessEntityBinarySensor(t *testing.T) {
	// bool value -> binary_sensor
	e := guessEntity("occupancy", true)
	if e.Component != ComponentBinarySensor {
		t.Errorf("bool field component = %q, want binary_sensor", e.Component)
	}

	// on/off-ish string values -> binary_sensor
	bin := []struct{ field, val string }{
		{"door_contact", "open"}, {"motion_detected", "motion"},
		{"window_status", "closed"}, {"occupied", "occupied"},
		{"link_quality", "connected"}, {"alarm_state", "alarm"},
	}
	for _, c := range bin {
		e = guessEntity(c.field, c.val)
		if e.Component != ComponentBinarySensor {
			t.Errorf("guessEntity(%q, %q) component = %q, want binary_sensor", c.field, c.val, e.Component)
		}
	}

	// device_class from field name
	if e := guessEntity("motion_detected", "motion"); e.DeviceClass != "motion" {
		t.Errorf("motion device_class = %q", e.DeviceClass)
	}
	if e := guessEntity("door_contact", "open"); e.DeviceClass != "door" {
		t.Errorf("door device_class = %q", e.DeviceClass)
	}
	if e := guessEntity("occupied", "occupied"); e.DeviceClass != "occupancy" {
		t.Errorf("occupancy device_class = %q", e.DeviceClass)
	}

	// plain strings stay sensor
	e = guessEntity("firmware", "1.2.3")
	if e.Component != ComponentSensor {
		t.Errorf("plain string component = %q, want sensor", e.Component)
	}
}

func TestGuessEntityMoreClasses(t *testing.T) {
	cases := []struct {
		field string
		dc    string
		unit  string
	}{
		{"humidity", "humidity", "%"},
		{"air_pressure", "pressure", "hPa"},
		{"battery_current", "current", "A"},
		{"daily_energy", "energy", "kWh"},
		{"wifi_signal", "signal_strength", "dBm"},
		{"light_lux", "illuminance", "lx"},
	}
	for _, c := range cases {
		e := guessEntity(c.field, 1.0)
		if e.DeviceClass != c.dc || e.Unit != c.unit {
			t.Errorf("guessEntity(%q) = dc=%q unit=%q, want dc=%q unit=%q",
				c.field, e.DeviceClass, e.Unit, c.dc, c.unit)
		}
	}
}

func TestBuildDiscoveryBinarySensor(t *testing.T) {
	dev := &Device{Topic: "home/sensors/room1", Name: "room1"}
	ent := Entity{Field: "motion_detected", Name: "motion_detected", Component: ComponentBinarySensor, DeviceClass: "motion"}

	topic, payload, err := BuildDiscovery("homeassistant", dev, ent, dev.Topic)
	if err != nil {
		t.Fatalf("BuildDiscovery: %v", err)
	}
	if !strings.Contains(topic, "/binary_sensor/") {
		t.Errorf("binary_sensor topic wrong: %s", topic)
	}
	body := string(payload)
	if !strings.Contains(body, `"payload_on":"ON"`) || !strings.Contains(body, `"payload_off":"OFF"`) {
		t.Errorf("payload_on/off missing: %s", body)
	}
	if !strings.Contains(body, `value_json.fields.motion_detected`) {
		t.Errorf("binary value_template missing: %s", body)
	}
	// binary_sensor must NOT carry unit_of_measurement
	if strings.Contains(body, "unit_of_measurement") {
		t.Errorf("binary_sensor should not have unit: %s", body)
	}
}

func TestBuildDiscoverySensorStateClass(t *testing.T) {
	dev := &Device{Topic: "home/ups/ups", Name: "ups"}
	ent := Entity{Field: "battery_voltage", Name: "battery_voltage", DeviceClass: "voltage", Unit: "V"}

	_, payload, err := BuildDiscovery("homeassistant", dev, ent, dev.Topic)
	if err != nil {
		t.Fatalf("BuildDiscovery: %v", err)
	}
	body := string(payload)
	if !strings.Contains(body, `"state_class":"measurement"`) {
		t.Errorf("sensor state_class missing: %s", body)
	}
	if !strings.Contains(body, `"unit_of_measurement":"V"`) {
		t.Errorf("unit missing: %s", body)
	}
}

func TestMigrationAddsComponentColumn(t *testing.T) {
	// simulate a pre-M3 database: entities table without component column
	db, err := openRawDB(t.TempDir() + "/old.db")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	db.Exec(`CREATE TABLE entities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id INTEGER NOT NULL,
		field TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		device_class TEXT NOT NULL DEFAULT '',
		unit TEXT NOT NULL DEFAULT '',
		icon TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		UNIQUE(device_id, field))`)
	db.Close()

	s, err := OpenStore(t.TempDir() + "/old.db")
	_ = s
	if err != nil {
		t.Fatalf("OpenStore on old db: %v", err)
	}
	var hasComp int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('entities') WHERE name='component'`).Scan(&hasComp)
	if hasComp == 0 {
		t.Error("component column not added by migration")
	}
}
