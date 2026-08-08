package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Bridge is the core mqtt2ha engine.
type Bridge struct {
	cfg    *Config
	store  *Store
	client mqtt.Client

	// discoveredIndex maps a node prefix (e.g. "zigbee2mqtt/") to whether
	// that node has published its own HA discovery message (seen via
	// homeassistant/+/+/config). In-memory, rebuilt on start.
	mu          sync.Mutex
	selfDiscIdx map[string]bool
}

// NewBridge creates a bridge from config + store.
func NewBridge(cfg *Config, store *Store) *Bridge {
	return &Bridge{
		cfg:         cfg,
		store:       store,
		selfDiscIdx: map[string]bool{},
	}
}

// Start connects to MQTT and subscribes.
func (b *Bridge) Start() error {
	opts := mqtt.NewClientOptions().
		AddBroker(b.cfg.MQTT.Broker).
		SetClientID(b.cfg.MQTT.ClientID + "_" + randSuffix()).
		SetUsername(b.cfg.MQTT.Username).
		SetPassword(b.cfg.MQTT.Password).
		SetKeepAlive(time.Duration(b.cfg.MQTT.KeepAlive) * time.Second).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetOrderMatters(false).
		SetOnConnectHandler(b.onConnect)

	b.client = mqtt.NewClient(opts)
	if token := b.client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", token.Error())
	}
	return nil
}

func (b *Bridge) onConnect(_ mqtt.Client) {
	// data topics
	for _, sub := range b.cfg.MQTT.Subscribe {
		t := b.client.Subscribe(sub, 0, b.onData)
		if t.Wait() && t.Error() != nil {
			log.Printf("subscribe %s: %v", sub, t.Error())
		} else {
			log.Printf("subscribed data topic: %s", sub)
		}
	}
	// discovery topic: HA supports both 4-segment and 5-segment formats
	//   homeassistant/<component>/<object_id>/config
	//   homeassistant/<component>/<device_id>/<object_id>/config
	discPrefix := b.cfg.MQTT.DiscoveryPrefix
	for _, pat := range []string{discPrefix + "/+/+/config", discPrefix + "/+/+/+/config"} {
		t := b.client.Subscribe(pat, 0, b.onSelfDiscovery)
		if t.Wait() && t.Error() != nil {
			log.Printf("subscribe %s: %v", pat, t.Error())
		} else {
			log.Printf("subscribed discovery topic: %s", pat)
		}
	}
}

// onSelfDiscovery records nodes that publish their own discovery, and
// retires any locally-pending devices for that prefix (delete + blacklist).
func (b *Bridge) onSelfDiscovery(_ mqtt.Client, msg mqtt.Message) {
	var cfg struct {
		StateTopic string `json:"state_topic"`
		UniqueID   string `json:"unique_id"`
		Device     struct {
			Identifiers []string `json:"identifiers"`
		} `json:"device"`
	}
	if err := json.Unmarshal(msg.Payload(), &cfg); err != nil {
		return
	}

	// ignore our own discovery messages (mqtt2ha_* unique_id / identifiers)
	if strings.HasPrefix(cfg.UniqueID, "mqtt2ha_") {
		return
	}
	for _, id := range cfg.Device.Identifiers {
		if strings.HasPrefix(id, "mqtt2ha_") {
			return
		}
	}

	prefix := prefixOf(cfg.StateTopic)
	if prefix == "" {
		return
	}
	b.mu.Lock()
	b.selfDiscIdx[cfg.StateTopic] = true
	b.mu.Unlock()
	log.Printf("self-discovery observed: prefix=%q (topic=%s)", prefix, msg.Topic())

	// retire pending devices whose data topic equals (or is under) the
	// state_topic of the self-published discovery. Blacklist the exact
	// state_topic — NOT the whole prefix — so unrelated data under the
	// same first segment (e.g. home/ups/ups vs home/ups/cp1000) survives.
	devs, err := b.store.ListDevices()
	if err != nil {
		log.Printf("list devices: %v", err)
		return
	}
	st := cfg.StateTopic
	for i := range devs {
		d := &devs[i]
		if d.Status != StatusPending {
			continue
		}
		if d.Topic == st || strings.HasPrefix(d.Topic, st+"/") {
			if err := b.store.AddBlacklist(st); err != nil {
				log.Printf("blacklist %s: %v", st, err)
			}
			if err := b.store.DeleteDevice(d.ID); err != nil {
				log.Printf("delete device %d: %v", d.ID, err)
			}
			log.Printf("node %s publishes its own discovery -> pending retired, %s blacklisted", d.Topic, st)
		}
	}
}

// onData handles a data message on a subscribed topic.
func (b *Bridge) onData(_ mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()

	// skip HA discovery messages themselves (homeassistant/.../config)
	if strings.HasPrefix(topic, b.cfg.MQTT.DiscoveryPrefix+"/") {
		return
	}

	prefix := prefixOf(topic)

	// blacklist check (exact topic or under a blacklisted prefix)
	bl, err := b.store.IsBlacklisted(topic)
	if err != nil {
		log.Printf("blacklist check: %v", err)
		return
	}
	if bl {
		return // silently filtered
	}

	// node self-published discovery already?
	b.mu.Lock()
	selfPublished := b.selfDiscIdx[topic]
	b.mu.Unlock()
	if selfPublished {
		// ensure it's blacklisted so we stop processing entirely
		if err := b.store.AddBlacklist(topic); err != nil {
			log.Printf("blacklist %s: %v", topic, err)
		}
		return
	}

	dev, err := b.store.GetDeviceByTopic(topic)
	if err != nil {
		// new device
		dev = &Device{
			Topic:     topic,
			Prefix:    prefix,
			Name:      topic,
			Status:    StatusPending,
			MsgCount:  0,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
	}

	// parse payload & re-infer
	inf, err := inferDevice(msg.Payload())
	if err != nil {
		return // non-JSON or unsupported payload — silently ignore
	}
	applyInferred(dev, inf)

	// build entities
	var ents []Entity
	for field, v := range inf.Fields {
		e := guessEntity(field, v)
		ents = append(ents, e)
	}
	if len(ents) == 0 {
		return
	}

	// upsert device
	saved, err := b.store.UpsertDevice(dev)
	if err != nil {
		log.Printf("upsert device %s: %v", topic, err)
		return
	}
	dev = saved
	if err := b.store.ReplaceEntities(dev.ID, ents); err != nil {
		log.Printf("replace entities %s: %v", topic, err)
		return
	}

	// increment observation counter
	n, err := b.store.IncrementMsgCount(dev.ID)
	if err != nil {
		log.Printf("increment count %s: %v", topic, err)
		return
	}
	log.Printf("[%s] msg#%d status=%s fields=%d", topic, n, dev.Status, len(ents))

	if dev.Status == StatusPending {
		// self-discovery re-check (may have arrived just before this message)
		b.mu.Lock()
		sp := b.selfDiscIdx[topic]
		b.mu.Unlock()
		if sp {
			if err := b.store.AddBlacklist(topic); err != nil {
				log.Printf("blacklist %s: %v", topic, err)
			}
			if err := b.store.DeleteDevice(dev.ID); err != nil {
				log.Printf("delete device %d: %v", dev.ID, err)
			}
			log.Printf("node %s self-published discovery -> retired+blacklisted", topic)
			return
		}

		switch b.cfg.Mode {
		case "approval":
			// stays pending until user approves via web UI
		default: // auto
			if n >= b.cfg.Observe {
				if err := b.publishDevice(dev); err != nil {
					log.Printf("publish device %s: %v", topic, err)
				} else {
					_ = b.store.UpdateDeviceStatus(dev.ID, StatusApproved)
					log.Printf("auto-approved after %d msgs: %s", n, topic)
				}
			}
		}
	}
}

// publishDevice publishes discovery messages for all entities of a device.
// Enabled entities get full discovery (retained); disabled entities get an
// empty retained payload so HA removes any previously published entity.
func (b *Bridge) publishDevice(dev *Device) error {
	ents, err := b.store.ListEntities(dev.ID)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.Enabled {
			// clear any previously published discovery for this entity
			uid := entityUniqueID(dev, e)
			component := e.Component
			if component == "" {
				component = ComponentSensor
			}
			topic := fmt.Sprintf("%s/%s/%s/config", b.cfg.MQTT.DiscoveryPrefix, component, uid)
			tok := b.client.Publish(topic, 1, true, []byte{})
			tok.Wait()
			if tok.Error() != nil {
				return tok.Error()
			}
			log.Printf("cleared discovery %s (entity disabled)", topic)
			continue
		}
		topic, payload, err := BuildDiscovery(dev, e, dev.Topic)
		if err != nil {
			return err
		}
		tok := b.client.Publish(topic, 1, true, payload)
		tok.Wait()
		if tok.Error() != nil {
			return tok.Error()
		}
		log.Printf("published discovery %s (%d bytes)", topic, len(payload))
	}
	return nil
}

// applyInferred copies inferred device info onto the device.
func applyInferred(d *Device, inf *InferredDevice) {
	if inf.Name != "" {
		d.Name = inf.Name
	}
	if inf.Manufacturer != "" {
		d.Manufacturer = inf.Manufacturer
	}
	if inf.Model != "" {
		d.Model = inf.Model
	}
	if inf.Serial != "" {
		d.Serial = inf.Serial
	}
}

// prefixOf returns "first/" + "/" from a topic, e.g. "home/ups/ups" -> "home/".
// If topic has no '/', returns topic + "/".
func prefixOf(topic string) string {
	if i := strings.Index(topic, "/"); i >= 0 {
		return topic[:i+1]
	}
	return topic + "/"
}

func randSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}
