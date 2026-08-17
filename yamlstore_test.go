package main

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestYamlStore(t *testing.T) *YamlStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewYamlStore(dir)
	if err != nil {
		t.Fatalf("NewYamlStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestYamlStoreUpsertPersists(t *testing.T) {
	s := newTestYamlStore(t)
	d, err := s.UpsertDevice(&Device{Topic: "home/ups/test", Name: "test", Status: StatusPending})
	if err != nil {
		t.Fatalf("UpsertDevice: %v", err)
	}
	if d.ID == 0 {
		t.Fatal("expected non-zero id")
	}
	// 文件应存在
	f := s.fileFor("home/ups/test")
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("yaml file not created: %v", err)
	}
	// 重新加载
	s2, err := NewYamlStore(s.dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer s2.Close()
	got, err := s2.GetDeviceByTopic("home/ups/test")
	if err != nil {
		t.Fatalf("GetDeviceByTopic after reload: %v", err)
	}
	if got.Name != "test" || got.Status != StatusPending {
		t.Fatalf("reload mismatch: %+v", got)
	}
	if got.ID != d.ID {
		t.Fatalf("id not stable across reload: got %d want %d", got.ID, d.ID)
	}
}

func TestYamlStoreStatusPersists(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/s1"})
	if err := s.UpdateDeviceStatus(d.ID, StatusApproved); err != nil {
		t.Fatalf("UpdateDeviceStatus: %v", err)
	}
	s2, _ := NewYamlStore(s.dir)
	defer s2.Close()
	got, _ := s2.GetDeviceByTopic("home/ups/s1")
	if got.Status != StatusApproved {
		t.Fatalf("status not persisted: %q", got.Status)
	}
}

func TestYamlStoreReplaceEntitiesHashNoWrite(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/s2"})
	ents := []Entity{{Field: "a", Name: "A", Component: "sensor", Unit: "%"}}
	if err := s.ReplaceEntities(d.ID, ents); err != nil {
		t.Fatalf("ReplaceEntities: %v", err)
	}
	f := s.fileFor("home/ups/s2")
	stat1, _ := os.Stat(f)
	// 相同集合再写——hash 相同不应写盘
	if err := s.ReplaceEntities(d.ID, ents); err != nil {
		t.Fatalf("ReplaceEntities (same): %v", err)
	}
	stat2, _ := os.Stat(f)
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Fatal("file rewritten despite identical entity set (no hash compare)")
	}
	// 字段集合变化（新增）才写盘
	ents2 := []Entity{{Field: "a", Name: "A", Component: "sensor", Unit: "%"},
		{Field: "b", Name: "B", Component: "sensor", Unit: "V"}}
	if err := s.ReplaceEntities(d.ID, ents2); err != nil {
		t.Fatalf("ReplaceEntities (added field): %v", err)
	}
	stat3, _ := os.Stat(f)
	if stat2.ModTime().Equal(stat3.ModTime()) {
		t.Fatal("file not rewritten after entity change")
	}
}

func TestYamlStoreMsgCountMemoryOnly(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/s3"})
	s.IncrementMsgCount(d.ID)
	s.IncrementMsgCount(d.ID)
	got, _ := s.GetDeviceByTopic("home/ups/s3")
	if got.MsgCount != 2 {
		t.Fatalf("msg_count: got %d want 2", got.MsgCount)
	}
	// 重启后归零（不写文件）
	s2, _ := NewYamlStore(s.dir)
	defer s2.Close()
	got2, _ := s2.GetDeviceByTopic("home/ups/s3")
	if got2.MsgCount != 0 {
		t.Fatalf("msg_count should reset on restart, got %d", got2.MsgCount)
	}
}

func TestYamlStoreBlacklist(t *testing.T) {
	s := newTestYamlStore(t)
	if err := s.AddBlacklist("zigbee2mqtt/"); err != nil {
		t.Fatalf("AddBlacklist: %v", err)
	}
	ok, _ := s.IsBlacklisted("zigbee2mqtt/bridge/health")
	if !ok {
		t.Fatal("expected blacklisted")
	}
	s2, _ := NewYamlStore(s.dir)
	defer s2.Close()
	ok2, _ := s2.IsBlacklisted("zigbee2mqtt/anything")
	if !ok2 {
		t.Fatal("blacklist not persisted")
	}
	// 黑名单文件是 yaml
	if _, err := os.Stat(filepath.Join(s.dir, "blacklist.yaml")); err != nil {
		t.Fatalf("blacklist.yaml missing: %v", err)
	}
}

func TestYamlStoreDeleteDevice(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/s4"})
	if err := s.DeleteDevice(d.ID); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if _, err := os.Stat(s.fileFor("home/ups/s4")); !os.IsNotExist(err) {
		t.Fatal("file should be removed")
	}
	if _, err := s.GetDeviceByTopic("home/ups/s4"); err == nil {
		t.Fatal("device should be gone")
	}
}

func TestYamlStoreImportSnapshot(t *testing.T) {
	s := newTestYamlStore(t)
	devs := []Device{{ID: 7, Topic: "home/ups/imp", Name: "imp", Status: StatusApproved}}
	ents := map[int64][]Entity{7: {{Field: "x", Name: "X", Unit: "V"}}}
	if err := s.ImportSnapshot(devs, ents, []string{"skip/"}); err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	s2, _ := NewYamlStore(s.dir)
	defer s2.Close()
	got, _ := s2.GetDeviceByTopic("home/ups/imp")
	if got.Name != "imp" || got.ID != 7 {
		t.Fatalf("import mismatch: %+v", got)
	}
	e, _ := s2.ListEntities(7)
	if len(e) != 1 || e[0].Unit != "V" {
		t.Fatalf("imported entities mismatch: %+v", e)
	}
	bl, _ := s2.ListBlacklist()
	if len(bl) != 1 || bl[0] != "skip/" {
		t.Fatalf("imported blacklist mismatch: %v", bl)
	}
	// 新设备 ID 从 8 开始
	d2, _ := s2.UpsertDevice(&Device{Topic: "home/ups/new"})
	if d2.ID != 8 {
		t.Fatalf("next id: got %d want 8", d2.ID)
	}
}

func TestYamlStoreOverrideMerge(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/ov"})
	// 用户编辑：load_watts unit=W（yaml 权威）
	user := []Entity{{Field: "load_watts", Name: "Load (W)", Component: "sensor", DeviceClass: "power", Unit: "W", Enabled: true}}
	if err := s.ReplaceEntities(d.ID, user); err != nil {
		t.Fatalf("ReplaceEntities (user): %v", err)
	}
	// 推断结果（unit=% 的旧 bug 行为）——不应覆盖 yaml
	inferred := []Entity{{Field: "load_watts", Component: "sensor", Unit: "%", Enabled: true}}
	if err := s.ReplaceEntities(d.ID, inferred); err != nil {
		t.Fatalf("ReplaceEntities (inferred): %v", err)
	}
	ents, _ := s.ListEntities(d.ID)
	if len(ents) != 1 || ents[0].Unit != "W" || ents[0].Name != "Load (W)" || ents[0].DeviceClass != "power" {
		t.Fatalf("override merge failed: %+v", ents)
	}
}

func TestYamlStoreReplaceEntitiesWithChange(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/chg"})
	ents := []Entity{{Field: "a", Component: "sensor", Unit: "%"}}
	changed, err := s.ReplaceEntitiesWithChange(d.ID, ents)
	if err != nil || !changed {
		t.Fatalf("first change: changed=%v err=%v", changed, err)
	}
	// 相同集合 → 不变化（不重发）
	changed, _ = s.ReplaceEntitiesWithChange(d.ID, ents)
	if changed {
		t.Fatal("identical set should report no change")
	}
	// 推断变化（%→W）被 yaml override 冻结后可能无变化——这里直接改集合验证
	ents2 := []Entity{{Field: "a", Component: "sensor", Unit: "W"}}
	changed, _ = s.ReplaceEntitiesWithChange(d.ID, ents2)
	if changed {
		t.Fatal("unit change should be frozen by yaml override (same field) -> no change")
	}
	// 新增字段 → 变化
	ents3 := []Entity{{Field: "a", Component: "sensor", Unit: "W"}, {Field: "b", Component: "sensor", Unit: "V"}}
	changed, _ = s.ReplaceEntitiesWithChange(d.ID, ents3)
	if !changed {
		t.Fatal("added field should report change")
	}
}

func TestYamlStoreYamlOnlyEntities(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/yonly"})
	// yaml 含一个推断不会上报的字段（手动新增）
	user := []Entity{{Field: "load_watts", Unit: "W", Enabled: true}, {Field: "manual_field", Unit: "V", Enabled: true}}
	if err := s.ReplaceEntities(d.ID, user); err != nil {
		t.Fatalf("user yaml: %v", err)
	}
	// 下一次消息：推断只报 load_watts（manual_field 不在推断集）
	inferred := []Entity{{Field: "load_watts", Unit: "%", Enabled: true}}
	if _, err := s.ReplaceEntitiesWithChange(d.ID, inferred); err != nil {
		t.Fatalf("inferred: %v", err)
	}
	ents, _ := s.ListEntities(d.ID)
	fields := map[string]string{}
	for _, e := range ents {
		fields[e.Field] = e.Unit
	}
	if fields["load_watts"] != "W" {
		t.Fatalf("load_watts unit not preserved: %+v", fields)
	}
	if _, ok := fields["manual_field"]; !ok {
		t.Fatalf("yaml-only entity lost after inferred ReplaceEntities: %+v", fields)
	}
}

func TestYamlStoreReloadDevice(t *testing.T) {
	s := newTestYamlStore(t)
	d, _ := s.UpsertDevice(&Device{Topic: "home/ups/rl"})
	s.ReplaceEntities(d.ID, []Entity{{Field: "a", Component: "sensor", Unit: "%", Enabled: true}})

	// 改完成文件（模拟用户编辑 override：unit %->W + 新增字段）
	f := s.fileFor("home/ups/rl")
	err := os.WriteFile(f, []byte("id: 5\ntopic: home/ups/rl\nstatus: approved\nentities:\n- field: a\n  component: sensor\n  unit: W\n- field: b\n  component: sensor\n  unit: V\n"), 0o644)
	if err != nil {
		t.Fatalf("write override: %v", err)
	}
	changed, err := s.ReloadDevice("home/ups/rl")
	if err != nil || !changed {
		t.Fatalf("reload changed: %v err=%v", changed, err)
	}
	ents, _ := s.ListEntities(d.ID)
	if len(ents) != 2 || ents[0].Unit != "W" {
		t.Fatalf("reloaded entities mismatch: %+v", ents)
	}
	// 再次 reload 相同 → 无变化
	changed, _ = s.ReloadDevice("home/ups/rl")
	if changed {
		t.Fatal("reload same file should report no change")
	}
}

func TestSanitizeTopic(t *testing.T) {
	if got := sanitizeTopic("home/ups/ai-server-ups"); got != "home_ups_ai-server-ups" {
		t.Fatalf("sanitize: %q", got)
	}
	if got := sanitizeTopic("zigbee2mqtt/bridge/#"); got != "zigbee2mqtt_bridge__" {
		t.Fatalf("sanitize wildcard: %q", got)
	}
}
