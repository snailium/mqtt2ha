package main

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDeviceLifecycle(t *testing.T) {
	s := openTestStore(t)

	d := &Device{Topic: "home/ups/ups", Prefix: "home/", Name: "ups"}
	got, err := s.UpsertDevice(d)
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	if got.Status != StatusPending {
		t.Errorf("new device status = %q, want %q", got.Status, StatusPending)
	}

	// upsert again keeps status, updates metadata
	d.Name = "Back-UPS NS 1500M2"
	d.Model = "Back-UPS NS 1500M2"
	if _, err := s.UpsertDevice(d); err != nil {
		t.Fatalf("UpsertDevice#2: %v", err)
	}
	fetched, err := s.GetDeviceByTopic("home/ups/ups")
	if err != nil {
		t.Fatalf("GetDeviceByTopic: %v", err)
	}
	if fetched.Model != "Back-UPS NS 1500M2" {
		t.Errorf("model = %q, want updated value", fetched.Model)
	}

	// msg counter
	for i := 0; i < 5; i++ {
		if _, err := s.IncrementMsgCount(fetched.ID); err != nil {
			t.Fatalf("IncrementMsgCount: %v", err)
		}
	}
	fetched, _ = s.GetDeviceByTopic("home/ups/ups")
	if fetched.MsgCount != 5 {
		t.Errorf("msg_count = %d, want 5", fetched.MsgCount)
	}

	// entities
	ents := []Entity{
		{Field: "battery_charge_percent", Name: "battery_charge_percent", DeviceClass: "battery", Unit: "%", Enabled: true},
		{Field: "ups_status", Name: "ups_status", Enabled: true},
	}
	if err := s.ReplaceEntities(fetched.ID, ents); err != nil {
		t.Fatalf("ReplaceEntities: %v", err)
	}
	list, err := s.ListEntities(fetched.ID)
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("entities = %d, want 2", len(list))
	}

	// status transition
	if err := s.UpdateDeviceStatus(fetched.ID, StatusApproved); err != nil {
		t.Fatalf("UpdateDeviceStatus: %v", err)
	}
	fetched, _ = s.GetDeviceByID(fetched.ID)
	if fetched.Status != StatusApproved {
		t.Errorf("status = %q, want %q", fetched.Status, StatusApproved)
	}

	// delete cascades entities
	if err := s.DeleteDevice(fetched.ID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if _, err := s.GetDeviceByTopic("home/ups/ups"); err == nil {
		t.Error("device still exists after delete")
	}
	left, _ := s.ListEntities(fetched.ID)
	if len(left) != 0 {
		t.Errorf("entities remain after delete: %d", len(left))
	}
}

func TestBlacklistMatching(t *testing.T) {
	s := openTestStore(t)

	if err := s.AddBlacklist("home/ups/ups"); err != nil {
		t.Fatalf("AddBlacklist: %v", err)
	}
	if err := s.AddBlacklist("zigbee2mqtt/"); err != nil {
		t.Fatalf("AddBlacklist#2: %v", err)
	}

	cases := []struct {
		topic string
		want  bool
	}{
		{"home/ups/ups", true},          // exact match
		{"home/ups/ups/extra", true},    // under blacklisted topic
		{"home/ups/cp1000", false},      // sibling survives
		{"home/ups", false},             // parent of blacklisted topic is NOT matched
		{"zigbee2mqtt/bridge/state", true}, // under blacklisted prefix
		{"telegraf/home-ai/cpu", false}, // unrelated
	}
	for _, c := range cases {
		got, err := s.IsBlacklisted(c.topic)
		if err != nil {
			t.Fatalf("IsBlacklisted(%q): %v", c.topic, err)
		}
		if got != c.want {
			t.Errorf("IsBlacklisted(%q) = %v, want %v", c.topic, got, c.want)
		}
	}
}

func TestListBlacklist(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddBlacklist("a/"); err != nil {
		t.Fatalf("AddBlacklist: %v", err)
	}
	if err := s.AddBlacklist("a/"); err != nil { // duplicate ignored
		t.Fatalf("AddBlacklist dup: %v", err)
	}
	bl, err := s.ListBlacklist()
	if err != nil {
		t.Fatalf("ListBlacklist: %v", err)
	}
	if len(bl) != 1 {
		t.Errorf("blacklist len = %d, want 1 (dedup)", len(bl))
	}
}
