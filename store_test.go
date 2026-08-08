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
		{"home/ups/ups", true},             // exact match
		{"home/ups/ups/extra", true},       // under blacklisted topic
		{"home/ups/cp1000", false},         // sibling survives
		{"home/ups", false},                // parent of blacklisted topic is NOT matched
		{"zigbee2mqtt/bridge/state", true}, // under blacklisted prefix
		{"telegraf/home-ai/cpu", false},    // unrelated
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

func TestUpdateEntityAndMeta(t *testing.T) {
	s := openTestStore(t)

	d := &Device{Topic: "home/ups/ups", Prefix: "home/", Name: "ups"}
	d, err := s.UpsertDevice(d)
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	ents := []Entity{{Field: "battery_charge_percent", Name: "battery_charge_percent", DeviceClass: "battery", Unit: "%", Enabled: true}}
	if err := s.ReplaceEntities(d.ID, ents); err != nil {
		t.Fatalf("ReplaceEntities: %v", err)
	}
	list, _ := s.ListEntities(d.ID)
	ent := list[0]

	// update entity
	ent.Name = "UPS Battery"
	ent.DeviceClass = ""
	ent.Unit = "percent"
	ent.Enabled = false
	if err := s.UpdateEntity(ent.ID, ent); err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	got, _ := s.ListEntities(d.ID)
	if got[0].Name != "UPS Battery" || got[0].Enabled || got[0].Unit != "percent" {
		t.Errorf("entity after update = %+v", got[0])
	}

	// update device meta
	if err := s.UpdateDeviceMeta(d.ID, "Back-UPS NS 1500M2", "NS 1500M2", "APC", "SN123"); err != nil {
		t.Fatalf("UpdateDeviceMeta: %v", err)
	}
	fetched, _ := s.GetDeviceByID(d.ID)
	if fetched.Name != "Back-UPS NS 1500M2" || fetched.Serial != "SN123" {
		t.Errorf("device after meta update = %+v", fetched)
	}
}

func TestBlacklistDelete(t *testing.T) {
	s := openTestStore(t)
	if err := s.AddBlacklist("zigbee2mqtt/"); err != nil {
		t.Fatalf("AddBlacklist: %v", err)
	}
	if err := s.DeleteBlacklist("zigbee2mqtt/"); err != nil {
		t.Fatalf("DeleteBlacklist: %v", err)
	}
	bl, _ := s.ListBlacklist()
	if len(bl) != 0 {
		t.Errorf("blacklist after delete = %v, want empty", bl)
	}
}

func TestImportSnapshot(t *testing.T) {
	s := openTestStore(t)

	// seed some state
	d1 := &Device{Topic: "home/ups/ups", Prefix: "home/", Name: "ups"}
	d1, _ = s.UpsertDevice(d1)
	_ = s.ReplaceEntities(d1.ID, []Entity{{Field: "battery_charge_percent", Name: "battery_charge_percent", Unit: "%", Enabled: true}})
	_ = s.AddBlacklist("zigbee2mqtt/")

	// import a fresh snapshot
	devs := []Device{
		{ID: 1, Topic: "home/ups/cp1000", Prefix: "home/", Name: "cp1000", Status: StatusApproved, MsgCount: 7},
	}
	ents := map[int64][]Entity{
		1: {{Field: "load_percent", Name: "load_percent", Unit: "%", Enabled: true}},
	}
	if err := s.ImportSnapshot(devs, ents, []string{"frigate/"}); err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}

	// old state gone
	if _, err := s.GetDeviceByTopic("home/ups/ups"); err == nil {
		t.Error("old device survived import")
	}
	bl, _ := s.ListBlacklist()
	if len(bl) != 1 || bl[0] != "frigate/" {
		t.Errorf("blacklist after import = %v", bl)
	}
	// new state present with entities linked
	got, _ := s.GetDeviceByTopic("home/ups/cp1000")
	if got.Status != StatusApproved || got.MsgCount != 7 {
		t.Errorf("imported device = %+v", got)
	}
	elist, _ := s.ListEntities(1)
	if len(elist) != 1 || elist[0].Field != "load_percent" {
		t.Errorf("imported entities = %+v", elist)
	}
}
