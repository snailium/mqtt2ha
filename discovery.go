package main

import (
	"encoding/json"
	"fmt"
)

// DiscoveryMsg is the HA MQTT discovery configuration message for one entity.
type DiscoveryMsg struct {
	Name              string          `json:"name"`
	StateTopic        string          `json:"state_topic"`
	ValueTemplate     string          `json:"value_template,omitempty"`
	UniqueID          string          `json:"unique_id"`
	DeviceClass       string          `json:"device_class,omitempty"`
	UnitOfMeasurement string          `json:"unit_of_measurement,omitempty"`
	StateClass        string          `json:"state_class,omitempty"`
	PayloadOn         string          `json:"payload_on,omitempty"`
	PayloadOff        string          `json:"payload_off,omitempty"`
	Icon              string          `json:"icon,omitempty"`
	Device            DiscoveryDevice `json:"device"`
	AvailabilityTopic string          `json:"availability_topic,omitempty"`
	AvailabilityTpl   string          `json:"availability_template,omitempty"`
}

// valueTemplateFor builds the value_template expression for a field,
// handling both flat JSON and Telegraf's nested {"fields":{...}} format.
func valueTemplateFor(field string) string {
	return fmt.Sprintf("{{ value_json.fields.%s | default(value_json.%s) }}", field, field)
}

// binaryValueTemplate normalizes any of the common on/off-ish payload values
// to "ON"/"OFF" so HA's binary_sensor state stays clean.
func binaryValueTemplate(field string) string {
	on := `['true','1','on','open','yes','occupied','motion','detected','connected','online','alarm','ok','warning','error','critical']`
	return fmt.Sprintf("{{ 'ON' if value_json.fields.%s | default(value_json.%s) in %s else 'OFF' }}", field, field, on)
}

// DiscoveryDevice is the device block inside a discovery message.
type DiscoveryDevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name,omitempty"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
	SerialNumber string   `json:"sn,omitempty"`
}

// deviceKey builds a stable, URL-safe identifier from a topic.
func deviceKey(topic string) string {
	return "mqtt2ha_" + sanitize(topic)
}

// sanitize keeps [a-zA-Z0-9_-], replacing everything else with '_'.
func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// entityUniqueID builds the stable unique_id for one entity.
// The "v2" prefix is load-bearing: HA caches discovery hashes by topic, so
// bump it (v2→v3) to force HA to recreate entities from scratch.
func entityUniqueID(dev *Device, ent Entity) string {
	return fmt.Sprintf("mqtt2ha_v2_%s_%s", sanitize(dev.Topic), sanitize(ent.Field))
}

// BuildDiscovery builds the discovery JSON for one entity of a device.
// Returns the discovery topic (homeassistant/<component>/<unique_id>/config) and payload.
func BuildDiscovery(dev *Device, ent Entity, dataTopic string) (string, []byte, error) {
	uid := entityUniqueID(dev, ent)
	component := ent.Component
	if component == "" {
		component = ComponentSensor
	}

	msg := DiscoveryMsg{
		Name:        ent.Name,
		StateTopic:  dataTopic,
		UniqueID:    uid,
		DeviceClass: ent.DeviceClass,
		Icon:        ent.Icon,
		Device: DiscoveryDevice{
			Identifiers:  []string{deviceKey(dev.Topic)},
			Name:         dev.Name,
			Manufacturer: dev.Manufacturer,
			Model:        dev.Model,
			SerialNumber: dev.Serial,
		},
	}

	switch component {
	case ComponentBinarySensor:
		// normalize payload to ON/OFF and let HA match against it
		msg.ValueTemplate = binaryValueTemplate(ent.Field)
		msg.PayloadOn = "ON"
		msg.PayloadOff = "OFF"
	default: // sensor
		msg.ValueTemplate = valueTemplateFor(ent.Field)
		msg.UnitOfMeasurement = ent.Unit
		if ent.Unit != "" {
			// state_class enables HA statistics/history for numeric sensors
			msg.StateClass = "measurement"
		}
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return "", nil, err
	}
	topic := fmt.Sprintf("%s/%s/%s/config", "homeassistant", component, uid)
	return topic, payload, nil
}
