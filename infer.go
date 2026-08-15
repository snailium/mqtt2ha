package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// InferredDevice is the result of parsing a raw JSON MQTT payload.
type InferredDevice struct {
	Name         string
	Manufacturer string
	Model        string
	Serial       string
	// Fields maps field name -> raw JSON value (number or string).
	Fields map[string]any
}

// inferDevice extracts device info + fields from a JSON payload.
// It understands the Telegraf JSON format: {"fields":{...},"tags":{...},"name":...,"timestamp":...}
// as well as flat JSON objects.
func inferDevice(payload []byte) (*InferredDevice, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}

	dev := &InferredDevice{Fields: map[string]any{}}

	// Telegraf format: fields + tags
	if fields, ok := raw["fields"].(map[string]any); ok {
		for k, v := range fields {
			if isUsableValue(v) {
				dev.Fields[k] = v
			}
		}
		if tags, ok := raw["tags"].(map[string]any); ok {
			for _, k := range []string{"model", "manufacturer", "serial", "ups_name", "device_name", "name"} {
				if v, ok := tags[k].(string); ok && v != "" {
					switch k {
					case "model":
						dev.Model = v
					case "manufacturer":
						dev.Manufacturer = v
					case "serial":
						dev.Serial = v
					case "ups_name", "device_name", "name":
						dev.Name = v
					}
				}
			}
		}
		if n, ok := raw["name"].(string); ok && dev.Name == "" {
			dev.Name = n
		}
		return dev, nil
	}

	// Flat JSON: every top-level key is a field
	for k, v := range raw {
		if k == "timestamp" {
			continue
		}
		if isUsableValue(v) {
			dev.Fields[k] = v
		}
	}
	return dev, nil
}

// isUsableValue returns true for numbers, bools and short strings.
func isUsableValue(v any) bool {
	switch t := v.(type) {
	case float64, bool:
		return true
	case string:
		// keep strings only if not obviously useless metadata
		if t == "" || len(t) > 80 {
			return false
		}
		return true
	case json.Number:
		return true
	default:
		return false
	}
}

// Component constants (HA MQTT component names).
const (
	ComponentSensor       = "sensor"
	ComponentBinarySensor = "binary_sensor"
)

// guessEntity infers HA entity attributes from a field name + value.
func guessEntity(field string, v any) Entity {
	e := Entity{
		Field:     field,
		Name:      field,
		Component: ComponentSensor,
		Enabled:   true,
	}
	lower := strings.ToLower(field)

	// boolean / on-off-ish values -> binary_sensor
	if _, ok := v.(bool); ok {
		e.Component = ComponentBinarySensor
		e.Icon = "mdi:power"
		return e
	}
	if s, ok := v.(string); ok {
		ls := strings.ToLower(s)
		switch ls {
		case "on", "off", "open", "closed", "true", "false", "yes", "no",
			"occupied", "unoccupied", "motion", "no_motion", "detected", "not_detected",
			"connected", "disconnected", "online", "offline", "alarm", "normal",
			"ok", "error", "warning", "critical":
			e.Component = ComponentBinarySensor
			e.Icon = "mdi:power"
			// device_class from the field name
			switch {
			case strings.Contains(lower, "motion"):
				e.DeviceClass = "motion"
			case strings.Contains(lower, "door"):
				e.DeviceClass = "door"
			case strings.Contains(lower, "window"):
				e.DeviceClass = "window"
			case strings.Contains(lower, "occup"):
				e.DeviceClass = "occupancy"
			case strings.Contains(lower, "moisture") || strings.Contains(lower, "wet"):
				e.DeviceClass = "moisture"
			case strings.Contains(lower, "connect"):
				e.DeviceClass = "connectivity"
			case strings.Contains(lower, "presence") || strings.Contains(lower, "motion"):
				e.DeviceClass = "presence"
			}
			return e
		}
	}

	// numeric?
	if isNumeric(v) {
		switch {
		case strings.Contains(lower, "load") && !strings.Contains(lower, "watts") && !strings.Contains(lower, "power"):
			// load is a plain percentage, not battery state of charge
			e.Unit = "%"
		case strings.Contains(lower, "voltage") || strings.HasSuffix(lower, "volt") || strings.HasSuffix(lower, "v"):
			e.DeviceClass = "voltage"
			e.Unit = "V"
		case strings.Contains(lower, "percent") || strings.Contains(lower, "charge") || strings.HasSuffix(lower, "pct"):
			e.DeviceClass = "battery"
			e.Unit = "%"
		case strings.Contains(lower, "runtime") || strings.Contains(lower, "time_left") || strings.Contains(lower, "time"):
			e.DeviceClass = "duration"
			e.Unit = "s"
		case strings.Contains(lower, "watts") || strings.Contains(lower, "watt") || (strings.Contains(lower, "power") && !strings.Contains(lower, "nominal")):
			e.DeviceClass = "power"
			e.Unit = "W"
		case strings.Contains(lower, "frequency") || strings.Contains(lower, "hz"):
			e.DeviceClass = "frequency"
			e.Unit = "Hz"
		case strings.Contains(lower, "temp"):
			e.DeviceClass = "temperature"
			e.Unit = "°C"
		case strings.Contains(lower, "humid"):
			e.DeviceClass = "humidity"
			e.Unit = "%"
		case strings.Contains(lower, "pressure") || strings.Contains(lower, "press"):
			e.DeviceClass = "pressure"
			e.Unit = "hPa"
		case strings.Contains(lower, "current") || strings.Contains(lower, "amp"):
			e.DeviceClass = "current"
			e.Unit = "A"
		case strings.Contains(lower, "energy") || strings.Contains(lower, "kwh") || strings.Contains(lower, "wh"):
			e.DeviceClass = "energy"
			e.Unit = "kWh"
		case strings.Contains(lower, "signal") || strings.Contains(lower, "rssi"):
			e.DeviceClass = "signal_strength"
			e.Unit = "dBm"
		case strings.Contains(lower, "illumin") || strings.Contains(lower, "lux"):
			e.DeviceClass = "illuminance"
			e.Unit = "lx"
		}
	} else {
		// string values: status fields
		switch {
		case strings.Contains(lower, "status"):
			e.DeviceClass = ""
			e.Icon = "mdi:power"
		case strings.Contains(lower, "state"):
			e.DeviceClass = ""
		}
	}
	return e
}

func isNumeric(v any) bool {
	switch v.(type) {
	case float64, int64, json.Number:
		return true
	case string:
		_, err := strconv.ParseFloat(v.(string), 64)
		return err == nil
	default:
		return false
	}
}
