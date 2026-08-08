package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DeviceStatus tracks the lifecycle of a discovered node.
const (
	StatusPending     = "pending"
	StatusApproved    = "approved"
	StatusBlacklisted = "blacklisted"
)

// Device is a discovered node (one MQTT data topic = one device).
type Device struct {
	ID           int64     `json:"id"`
	Topic        string    `json:"topic"`
	Prefix       string    `json:"prefix"` // first path segment + "/" for blacklist matching
	Name         string    `json:"name"`
	Manufacturer string    `json:"manufacturer"`
	Model        string    `json:"model"`
	Serial       string    `json:"serial"`
	Status       string    `json:"status"`
	MsgCount     int       `json:"msg_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Entity is one sensor entity under a device.
type Entity struct {
	ID          int64  `json:"id"`
	DeviceID    int64  `json:"device_id"`
	Field       string `json:"field"`
	Name        string `json:"name"`
	DeviceClass string `json:"device_class,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// OpenStore opens (and creates) the SQLite database at path.
func OpenStore(path string) (*Store, error) {
	// busy_timeout avoids SQLITE_BUSY under concurrent MQTT callbacks
	dsn := path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS devices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			topic TEXT NOT NULL UNIQUE,
			prefix TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			manufacturer TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			serial TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			msg_count INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS entities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			device_id INTEGER NOT NULL,
			field TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			device_class TEXT NOT NULL DEFAULT '',
			unit TEXT NOT NULL DEFAULT '',
			icon TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			UNIQUE(device_id, field)
		)`,
		`CREATE TABLE IF NOT EXISTS blacklist (
			prefix TEXT PRIMARY KEY,
			created_at DATETIME NOT NULL
		)`,
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// UpsertDevice inserts a new device or updates existing one, returning the device.
func (s *Store) UpsertDevice(d *Device) (*Device, error) {
	now := time.Now()
	status := d.Status
	if status == "" {
		status = StatusPending
	}
	_, err := s.db.Exec(`
		INSERT INTO devices (topic, prefix, name, manufacturer, model, serial, status, msg_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(topic) DO UPDATE SET
			prefix=excluded.prefix,
			name=excluded.name,
			manufacturer=excluded.manufacturer,
			model=excluded.model,
			serial=excluded.serial,
			updated_at=excluded.updated_at`,
		d.Topic, d.Prefix, d.Name, d.Manufacturer, d.Model, d.Serial, status, d.MsgCount, now, now)
	if err != nil {
		return nil, err
	}
	return s.GetDeviceByTopic(d.Topic)
}

// GetDeviceByTopic fetches a device by its data topic.
func (s *Store) GetDeviceByTopic(topic string) (*Device, error) {
	row := s.db.QueryRow(`
		SELECT id, topic, prefix, name, manufacturer, model, serial, status, msg_count, created_at, updated_at
		FROM devices WHERE topic = ?`, topic)
	var d Device
	err := row.Scan(&d.ID, &d.Topic, &d.Prefix, &d.Name, &d.Manufacturer, &d.Model, &d.Serial, &d.Status, &d.MsgCount, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// GetDeviceByID fetches a device by primary key.
func (s *Store) GetDeviceByID(id int64) (*Device, error) {
	row := s.db.QueryRow(`
		SELECT id, topic, prefix, name, manufacturer, model, serial, status, msg_count, created_at, updated_at
		FROM devices WHERE id = ?`, id)
	var d Device
	err := row.Scan(&d.ID, &d.Topic, &d.Prefix, &d.Name, &d.Manufacturer, &d.Model, &d.Serial, &d.Status, &d.MsgCount, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDevices returns all devices ordered by id.
func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(`
		SELECT id, topic, prefix, name, manufacturer, model, serial, status, msg_count, created_at, updated_at
		FROM devices ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Topic, &d.Prefix, &d.Name, &d.Manufacturer, &d.Model, &d.Serial, &d.Status, &d.MsgCount, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateDeviceStatus updates status and bumps updated_at.
func (s *Store) UpdateDeviceStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE devices SET status=?, updated_at=? WHERE id=?`, status, time.Now(), id)
	return err
}

// UpdateDeviceMeta updates editable device metadata (name/model/manufacturer/serial).
func (s *Store) UpdateDeviceMeta(id int64, name, model, manufacturer, serial string) error {
	_, err := s.db.Exec(`UPDATE devices SET name=?, model=?, manufacturer=?, serial=?, updated_at=? WHERE id=?`,
		name, model, manufacturer, serial, time.Now(), id)
	return err
}

// UpdateEntity updates editable entity attributes (M2: edit before publish).
func (s *Store) UpdateEntity(id int64, e Entity) error {
	_, err := s.db.Exec(`UPDATE entities SET name=?, device_class=?, unit=?, icon=?, enabled=? WHERE id=?`,
		e.Name, e.DeviceClass, e.Unit, e.Icon, e.Enabled, id)
	return err
}

// IncrementMsgCount adds 1 to msg_count for a device.
func (s *Store) IncrementMsgCount(id int64) (int, error) {
	res, err := s.db.Exec(`UPDATE devices SET msg_count=msg_count+1, updated_at=? WHERE id=?`, time.Now(), id)
	if err != nil {
		return 0, err
	}
	_ = res
	d, err := s.GetDeviceByID(id)
	if err != nil {
		return 0, err
	}
	return d.MsgCount, nil
}

// DeleteDevice removes a device and its entities.
func (s *Store) DeleteDevice(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM entities WHERE device_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM devices WHERE id=?`, id)
	return err
}

// ReplaceEntities deletes all entities of a device and inserts new ones.
func (s *Store) ReplaceEntities(deviceID int64, ents []Entity) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM entities WHERE device_id=?`, deviceID); err != nil {
		return err
	}
	for _, e := range ents {
		e.DeviceID = deviceID
		_, err := tx.Exec(`
			INSERT INTO entities (device_id, field, name, device_class, unit, icon, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.DeviceID, e.Field, e.Name, e.DeviceClass, e.Unit, e.Icon, e.Enabled)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListEntities returns all entities for a device.
func (s *Store) ListEntities(deviceID int64) ([]Entity, error) {
	rows, err := s.db.Query(`
		SELECT id, device_id, field, name, device_class, unit, icon, enabled
		FROM entities WHERE device_id=? ORDER BY id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.DeviceID, &e.Field, &e.Name, &e.DeviceClass, &e.Unit, &e.Icon, &e.Enabled); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// IsBlacklisted checks whether a topic is blacklisted.
// Matching: exact topic, or topic falls under a blacklisted prefix.
// Blacklist entries may carry a trailing slash ("zigbee2mqtt/") or be an
// exact topic ("home/ups/ups"); both are normalized for matching.
func (s *Store) IsBlacklisted(topic string) (bool, error) {
	rows, err := s.db.Query(`SELECT prefix FROM blacklist`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var bl string
		if err := rows.Scan(&bl); err != nil {
			return false, err
		}
		bl = strings.TrimSuffix(bl, "/")
		if topic == bl || strings.HasPrefix(topic, bl+"/") {
			return true, nil
		}
	}
	return false, rows.Err()
}

// AddBlacklist adds a prefix to the blacklist.
func (s *Store) AddBlacklist(prefix string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO blacklist (prefix, created_at) VALUES (?, ?)`, prefix, time.Now())
	return err
}

// DeleteBlacklist removes a prefix from the blacklist.
func (s *Store) DeleteBlacklist(prefix string) error {
	_, err := s.db.Exec(`DELETE FROM blacklist WHERE prefix=?`, prefix)
	return err
}

// ListBlacklist returns all blacklisted prefixes.
func (s *Store) ListBlacklist() ([]string, error) {
	rows, err := s.db.Query(`SELECT prefix FROM blacklist ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ImportSnapshot atomically replaces all devices, entities and blacklist
// entries with the given snapshot (M2: config export/import).
// Device IDs are preserved so entity device_id references stay valid.
func (s *Store) ImportSnapshot(devs []Device, entsByDevice map[int64][]Entity, blacklist []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM entities`,
		`DELETE FROM devices`,
		`DELETE FROM blacklist`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for _, d := range devs {
		if _, err := tx.Exec(`
			INSERT INTO devices (id, topic, prefix, name, manufacturer, model, serial, status, msg_count, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.Topic, d.Prefix, d.Name, d.Manufacturer, d.Model, d.Serial, d.Status, d.MsgCount, d.CreatedAt, d.UpdatedAt); err != nil {
			return err
		}
		for _, e := range entsByDevice[d.ID] {
			if _, err := tx.Exec(`
				INSERT INTO entities (device_id, field, name, device_class, unit, icon, enabled)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				d.ID, e.Field, e.Name, e.DeviceClass, e.Unit, e.Icon, e.Enabled); err != nil {
				return err
			}
		}
	}
	for _, p := range blacklist {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO blacklist (prefix, created_at) VALUES (?, ?)`, p, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
