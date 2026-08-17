package main

// YamlStore — yaml-backed Store implementation.
//
// Design (per project decision 2026-08-15):
//   - One yaml file per MQTT topic under DevicesDir (default "devices/").
//   - The yaml file is the authoritative config: device meta + entity
//     attributes. Human-editable, git-friendly, hot-reloadable later.
//   - msg_count is memory-only (diagnostic counter, reset on restart).
//   - status is persisted back to the yaml file (low frequency).
//   - ReplaceEntities only writes to disk when the entity set actually
//     changed (hash compare) — no write amplification per message.
//   - Writes use temp file + rename for crash safety.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlDevice is the on-disk representation of one device file.
type yamlDevice struct {
	ID           int64        `yaml:"id"`
	Topic        string       `yaml:"topic"`
	Status       string       `yaml:"status"`
	Name         string       `yaml:"name,omitempty"`
	Manufacturer string       `yaml:"manufacturer,omitempty"`
	Model        string       `yaml:"model,omitempty"`
	Serial       string       `yaml:"serial,omitempty"`
	Entities     []yamlEntity `yaml:"entities"`
}

type yamlEntity struct {
	Field       string `yaml:"field"`
	Name        string `yaml:"name,omitempty"`
	Component   string `yaml:"component,omitempty"`
	DeviceClass string `yaml:"device_class,omitempty"`
	Unit        string `yaml:"unit,omitempty"`
	Icon        string `yaml:"icon,omitempty"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
}

// YamlStore implements Store using per-topic yaml files + in-memory maps.
type YamlStore struct {
	mu        sync.RWMutex
	dir       string
	devices   map[string]*Device   // key: topic
	entities  map[int64][]Entity   // key: device id
	entsHash  map[int64]string     // key: device id — sha256 of entity set
	blacklist map[string]time.Time // prefix -> created
	nextID    int64
}

// NewYamlStore creates the store, creating dir if needed and loading all
// devices/*.yaml.
func NewYamlStore(dir string) (*YamlStore, error) {
	if dir == "" {
		dir = "devices"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("yaml store: create dir: %w", err)
	}
	s := &YamlStore{
		dir:       dir,
		devices:   map[string]*Device{},
		entities:  map[int64][]Entity{},
		entsHash:  map[int64]string{},
		blacklist: map[string]time.Time{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.loadBlacklist(); err != nil {
		return nil, err
	}
	return s, nil
}

// sanitizeTopic converts a topic path to a safe file name.
func sanitizeTopic(topic string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", " ", "_", "#", "_", "+", "_", "*", "_")
	return r.Replace(topic)
}

func (s *YamlStore) fileFor(topic string) string {
	return filepath.Join(s.dir, sanitizeTopic(topic)+".yaml")
}

// topicForFile resolves a device yaml file path back to its topic by scanning
// known devices (avoids sanitize ambiguity when topics contain underscores/+/#).
func (s *YamlStore) topicForFile(fp string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for topic := range s.devices {
		if s.fileFor(topic) == fp {
			return topic
		}
	}
	return ""
}

func (s *YamlStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	maxID := int64(0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		yd := yamlDevice{}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &yd); err != nil {
			return fmt.Errorf("yaml store: parse %s: %w", e.Name(), err)
		}
		if yd.Topic == "" {
			continue
		}
		st := yd.Status
		if st == "" {
			st = StatusApproved
		}
		dev := &Device{
			ID: yd.ID, Topic: yd.Topic, Prefix: firstSegment(yd.Topic),
			Name: yd.Name, Manufacturer: yd.Manufacturer,
			Model: yd.Model, Serial: yd.Serial, Status: st,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		s.devices[yd.Topic] = dev
		ents := make([]Entity, 0, len(yd.Entities))
		for _, ye := range yd.Entities {
			comp := ye.Component
			if comp == "" {
				comp = ComponentSensor
			}
			en := true
			if ye.Enabled != nil {
				en = *ye.Enabled
			}
			ents = append(ents, Entity{
				Field: ye.Field, Name: ye.Name, Component: comp,
				DeviceClass: ye.DeviceClass, Unit: ye.Unit, Icon: ye.Icon,
				Enabled: en,
			})
		}
		s.entities[dev.ID] = ents
		s.entsHash[dev.ID] = hashEntities(ents)
		if dev.ID > maxID {
			maxID = dev.ID
		}
	}
	s.nextID = maxID + 1
	return nil
}

func firstSegment(topic string) string {
	if i := strings.Index(topic, "/"); i > 0 {
		return topic[:i+1]
	}
	return topic + "/"
}

func hashEntities(ents []Entity) string {
	parts := make([]string, 0, len(ents))
	for _, e := range ents {
		parts = append(parts, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t",
			e.Field, e.Name, e.Component, e.DeviceClass, e.Unit, e.Icon, e.Enabled))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// saveDevice writes one device file (temp + rename). Caller holds mu.
func (s *YamlStore) saveDevice(dev *Device) error {
	ents := s.entities[dev.ID]
	yd := yamlDevice{
		ID: dev.ID, Topic: dev.Topic, Status: dev.Status,
		Name: dev.Name, Manufacturer: dev.Manufacturer,
		Model: dev.Model, Serial: dev.Serial,
	}
	for _, e := range ents {
		en := e.Enabled
		yd.Entities = append(yd.Entities, yamlEntity{
			Field: e.Field, Name: e.Name, Component: e.Component,
			DeviceClass: e.DeviceClass, Unit: e.Unit, Icon: e.Icon,
			Enabled: &en,
		})
	}
	data, err := yaml.Marshal(yd)
	if err != nil {
		return err
	}
	f := s.fileFor(dev.Topic)
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// --- Store interface ---

func (s *YamlStore) Close() error { return nil }

func (s *YamlStore) UpsertDevice(d *Device) (*Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if existing, ok := s.devices[d.Topic]; ok {
		changed := false
		if d.Name != "" && d.Name != existing.Name {
			existing.Name = d.Name
			changed = true
		}
		if d.Manufacturer != "" && d.Manufacturer != existing.Manufacturer {
			existing.Manufacturer = d.Manufacturer
			changed = true
		}
		if d.Model != "" && d.Model != existing.Model {
			existing.Model = d.Model
			changed = true
		}
		if d.Serial != "" && d.Serial != existing.Serial {
			existing.Serial = d.Serial
			changed = true
		}
		if d.Status != "" && d.Status != existing.Status {
			existing.Status = d.Status
			changed = true
		}
		existing.UpdatedAt = now
		if changed {
			if err := s.saveDevice(existing); err != nil {
				return nil, err
			}
		}
		return existing, nil
	}
	id := s.nextID
	s.nextID++
	dev := &Device{
		ID: id, Topic: d.Topic, Prefix: firstSegment(d.Topic),
		Name: d.Name, Manufacturer: d.Manufacturer,
		Model: d.Model, Serial: d.Serial,
		Status:    d.Status,
		CreatedAt: now, UpdatedAt: now,
	}
	if dev.Status == "" {
		dev.Status = StatusPending
	}
	s.devices[dev.Topic] = dev
	s.entities[id] = nil
	s.entsHash[id] = hashEntities(nil)
	if err := s.saveDevice(dev); err != nil {
		return nil, err
	}
	return dev, nil
}

func (s *YamlStore) GetDeviceByTopic(topic string) (*Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[topic]
	if !ok {
		return nil, errNotFound(topic)
	}
	cp := *d
	return &cp, nil
}

func (s *YamlStore) GetDeviceByID(id int64) (*Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.devices {
		if d.ID == id {
			cp := *d
			return &cp, nil
		}
	}
	return nil, errNotFound(fmt.Sprintf("id %d", id))
}

func (s *YamlStore) ListDevices() ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *YamlStore) UpdateDeviceStatus(id int64, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id {
			d.Status = status
			d.UpdatedAt = time.Now()
			return s.saveDevice(d)
		}
	}
	return errNotFound(fmt.Sprintf("id %d", id))
}

func (s *YamlStore) UpdateDeviceMeta(id int64, name, model, manufacturer, serial string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id {
			d.Name, d.Model, d.Manufacturer, d.Serial = name, model, manufacturer, serial
			d.UpdatedAt = time.Now()
			return s.saveDevice(d)
		}
	}
	return errNotFound(fmt.Sprintf("id %d", id))
}

func (s *YamlStore) UpdateEntity(id int64, e Entity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ents := range s.entities {
		for i := range ents {
			if ents[i].ID == id {
				ents[i] = e
				s.entsHash[id] = hashEntities(ents)
				return s.saveDevice(s.devicesByID(id))
			}
		}
	}
	return errNotFound(fmt.Sprintf("entity id %d", id))
}

// devicesByID returns the device for an entity id — caller holds mu.
func (s *YamlStore) devicesByID(id int64) *Device {
	for _, d := range s.devices {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (s *YamlStore) IncrementMsgCount(id int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id {
			d.MsgCount++
			return d.MsgCount, nil
		}
	}
	return 0, errNotFound(fmt.Sprintf("id %d", id))
}

func (s *YamlStore) DeleteDevice(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for topic, d := range s.devices {
		if d.ID == id {
			delete(s.devices, topic)
			delete(s.entities, id)
			delete(s.entsHash, id)
			if err := os.Remove(s.fileFor(topic)); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
	}
	return errNotFound(fmt.Sprintf("id %d", id))
}

func (s *YamlStore) ReplaceEntities(deviceID int64, ents []Entity) error {
	_, err := s.ReplaceEntitiesWithChange(deviceID, ents)
	return err
}

// ReplaceEntitiesWithChange merges inferred entities with stored yaml
// overrides, persists when the effective set changed, and reports the change
// (drives re-discovery for approved devices).
func (s *YamlStore) ReplaceEntitiesWithChange(deviceID int64, ents []Entity) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.devicesByID(deviceID)
	if d == nil {
		return false, errNotFound(fmt.Sprintf("device id %d", deviceID))
	}
	// Override merge: the yaml file is the authoritative config. For every
	// inferred entity, keep user-edited attributes from the yaml file when
	// they are non-empty (or differ from the inferred default).
	existing := s.entities[deviceID]
	merged := make([]Entity, 0, len(ents))
	for _, e := range ents {
		if old, ok := findEntity(existing, e.Field); ok {
			if old.Name != "" {
				e.Name = old.Name
			}
			if old.Unit != "" {
				e.Unit = old.Unit
			}
			if old.DeviceClass != "" {
				e.DeviceClass = old.DeviceClass
			}
			if old.Icon != "" {
				e.Icon = old.Icon
			}
			e.Enabled = old.Enabled
			// component: yaml wins unless it's the default we would infer anyway
			if old.Component != "" && old.Component != ComponentSensor {
				e.Component = old.Component
			}
		}
		merged = append(merged, e)
	}
	for i := range merged {
		merged[i].ID = int64(i + 1)
	}
	// Hash compare — only write to disk when the set actually changed.
	h := hashEntities(merged)
	if h == s.entsHash[deviceID] {
		return false, nil
	}
	s.entities[deviceID] = merged
	s.entsHash[deviceID] = h
	return true, s.saveDevice(d)
}

// ReloadDevice re-reads a device's yaml file (hot-reload). Updates the
// in-memory device meta + entity set and reports whether the entity set
// changed (so the bridge can re-publish discovery).
func (s *YamlStore) ReloadDevice(topic string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.fileFor(topic))
	if err != nil {
		return false, err
	}
	var yd yamlDevice
	if err := yaml.Unmarshal(data, &yd); err != nil {
		return false, fmt.Errorf("reload %s: %w", topic, err)
	}
	dev, ok := s.devices[topic]
	if !ok {
		// new device appeared on disk (e.g. hand-written) — adopt it
		dev = &Device{ID: s.nextID, Topic: yd.Topic, Prefix: firstSegment(topic)}
		s.nextID++
		s.devices[topic] = dev
		s.entities[dev.ID] = nil
		s.entsHash[dev.ID] = hashEntities(nil)
	}
	if yd.Status != "" {
		dev.Status = yd.Status
	}
	dev.Name = yd.Name
	dev.Manufacturer = yd.Manufacturer
	dev.Model = yd.Model
	dev.Serial = yd.Serial
	dev.UpdatedAt = time.Now()

	ents := make([]Entity, 0, len(yd.Entities))
	for _, ye := range yd.Entities {
		comp := ye.Component
		if comp == "" {
			comp = ComponentSensor
		}
		en := true
		if ye.Enabled != nil {
			en = *ye.Enabled
		}
		ents = append(ents, Entity{
			Field: ye.Field, Name: ye.Name, Component: comp,
			DeviceClass: ye.DeviceClass, Unit: ye.Unit, Icon: ye.Icon, Enabled: en,
		})
	}
	for i := range ents {
		ents[i].ID = int64(i + 1)
	}
	h := hashEntities(ents)
	changed := h != s.entsHash[dev.ID]
	s.entities[dev.ID] = ents
	s.entsHash[dev.ID] = h
	return changed, nil
}

func findEntity(ents []Entity, field string) (Entity, bool) {
	for _, e := range ents {
		if e.Field == field {
			return e, true
		}
	}
	return Entity{}, false
}

func (s *YamlStore) ListEntities(deviceID int64) ([]Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ents, ok := s.entities[deviceID]
	if !ok {
		return nil, errNotFound(fmt.Sprintf("device id %d", deviceID))
	}
	out := make([]Entity, len(ents))
	copy(out, ents)
	return out, nil
}

func (s *YamlStore) IsBlacklisted(topic string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for prefix := range s.blacklist {
		if strings.HasPrefix(topic, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func (s *YamlStore) AddBlacklist(prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blacklist[prefix] = time.Now()
	return s.saveBlacklist()
}

func (s *YamlStore) DeleteBlacklist(prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blacklist, prefix)
	return s.saveBlacklist()
}

func (s *YamlStore) ListBlacklist() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.blacklist))
	for p := range s.blacklist {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// saveBlacklist writes blacklist.yaml in the devices dir. Caller holds mu.
func (s *YamlStore) saveBlacklist() error {
	ps := make([]string, 0, len(s.blacklist))
	for p := range s.blacklist {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	data, err := yaml.Marshal(map[string]any{"blacklist": ps})
	if err != nil {
		return err
	}
	f := filepath.Join(s.dir, "blacklist.yaml")
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// loadBlacklist reads blacklist.yaml (called from load()).
func (s *YamlStore) loadBlacklist() error {
	data, err := os.ReadFile(filepath.Join(s.dir, "blacklist.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var m struct {
		Blacklist []string `yaml:"blacklist"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return err
	}
	for _, p := range m.Blacklist {
		s.blacklist[p] = time.Now()
	}
	return nil
}

func (s *YamlStore) ImportSnapshot(devs []Device, entsByDevice map[int64][]Entity, blacklist []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	maxID := int64(0)
	for _, d := range devs {
		if d.ID > maxID {
			maxID = d.ID
		}
	}
	s.nextID = maxID + 1
	for _, d := range devs {
		cp := d
		cp.CreatedAt = time.Now()
		cp.UpdatedAt = time.Now()
		if cp.Status == "" {
			cp.Status = StatusPending
		}
		s.devices[cp.Topic] = &cp
		ents := entsByDevice[d.ID]
		s.entities[cp.ID] = ents
		s.entsHash[cp.ID] = hashEntities(ents)
		if err := s.saveDevice(&cp); err != nil {
			return err
		}
	}
	s.blacklist = map[string]time.Time{}
	for _, p := range blacklist {
		s.blacklist[p] = time.Now()
	}
	return s.saveBlacklist()
}

// errNotFound is a small sentinel to keep web.go error paths unchanged.
type notFoundErr struct{ msg string }

func (e notFoundErr) Error() string { return "not found: " + e.msg }

func errNotFound(msg string) error { return notFoundErr{msg} }
