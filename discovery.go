package main

import (
	"encoding/json"
	"fmt"
)

// DiscoveryMsg is the HA MQTT discovery configuration message for one sensor.
type DiscoveryMsg struct {
	Name              string          `json:"name"`
	StateTopic        string          `json:"state_topic"`
	ValueTemplate     string          `json:"value_template,omitempty"`
	UniqueID          string          `json:"unique_id"`
	DeviceClass       string          `json:"device_class,omitempty"`
	UnitOfMeasurement string          `json:"unit_of_measurement,omitempty"`
	Icon              string          `json:"icon,omitempty"`
	Device            DiscoveryDevice `json:"device"`
	AvailabilityTopic string          `json:"availability_topic,omitempty"`
	AvailabilityTpl   string          `json:"availability_template,omitempty"`
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

// BuildDiscovery builds the discovery JSON for one entity of a device.
// Returns the discovery topic (homeassistant/sensor/<unique_id>/config) and payload.
func BuildDiscovery(dev *Device, ent Entity, dataTopic string) (string, []byte, error) {
	uid := fmt.Sprintf("mqtt2ha_v2_%s_%s", sanitize(dev.Topic), sanitize(ent.Field))

	// value_template must handle both flat JSON and Telegraf's nested
	// {"fields":{...}} format: prefer fields.<field>, fall back to <field>.
	vt := fmt.Sprintf("{{ value_json.fields.%s | default(value_json.%s) }}", ent.Field, ent.Field)

	msg := DiscoveryMsg{
		Name:              ent.Name,
		StateTopic:        dataTopic,
		ValueTemplate:     vt,
		UniqueID:          uid,
		DeviceClass:       ent.DeviceClass,
		UnitOfMeasurement: ent.Unit,
		Icon:              ent.Icon,
		Device: DiscoveryDevice{
			Identifiers:  []string{deviceKey(dev.Topic)},
			Name:         dev.Name,
			Manufacturer: dev.Manufacturer,
			Model:        dev.Model,
			SerialNumber: dev.Serial,
		},
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return "", nil, err
	}
	topic := fmt.Sprintf("%s/sensor/%s/config", "homeassistant", uid)
	return topic, payload, nil
}
