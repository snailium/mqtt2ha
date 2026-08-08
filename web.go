package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// RegisterWeb wires the M2 web UI routes.
func (b *Bridge) RegisterWeb(mux *http.ServeMux) {
	mux.HandleFunc("/", b.handleIndex)
	mux.HandleFunc("/api/devices", b.handleAPI)
	mux.HandleFunc("/approve/", b.handleApprove)
	mux.HandleFunc("/reject/", b.handleReject)
	mux.HandleFunc("/blacklist/", b.handleBlacklist)
	mux.HandleFunc("/refresh/", b.handleRefresh)
	// M2: edit + export/import
	mux.HandleFunc("/api/entity/", b.handleEntityUpdate)
	mux.HandleFunc("/api/device/", b.handleDeviceUpdate)
	mux.HandleFunc("/api/export", b.handleExport)
	mux.HandleFunc("/api/import", b.handleImport)
	mux.HandleFunc("/api/blacklist/delete", b.handleBlacklistDelete)
}

var indexTpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>mqtt2ha</title>
<style>
body{font-family:sans-serif;max-width:1100px;margin:20px auto;padding:0 10px}
table{border-collapse:collapse;width:100%}
td,th{border:1px solid #ccc;padding:6px 10px;text-align:left}
.pending{color:#b58900}.approved{color:#2e7d32}.blacklisted{color:#c62828}
a.btn{margin-right:6px;text-decoration:none;border:1px solid #888;padding:2px 8px;border-radius:3px;color:#333;background:#f5f5f5}
a.btn:hover{background:#e0e0e0}
form.inline{display:inline}
input[type=text]{width:110px;padding:2px 4px}
input.wide{width:220px}
.entity-row{background:#fafafa;font-size:13px}
.entity-row input[type=text]{font-size:12px}
.toolbar{margin:12px 0}
.bl-item{display:inline-block;border:1px solid #bbb;padding:1px 8px;margin:2px;border-radius:3px;background:#fff0f0;font-size:13px}
.bl-item form{display:inline}
code{background:#eee;padding:1px 4px}
</style></head><body>
<h1>mqtt2ha — 节点管理 (mode: {{.Mode}})</h1>
<div class="toolbar">
  <a class="btn" href="/api/export">导出配置</a>
  <form class="inline" method="post" action="/api/import" enctype="multipart/form-data">
    <input type="file" name="file" accept=".json" required>
    <button type="submit">导入配置</button>
  </form>
</div>
<h3>黑名单:</h3>
{{range .Blacklist}}
  <span class="bl-item"><code>{{.}}</code>
  <form class="inline" method="post" action="/api/blacklist/delete"><input type="hidden" name="prefix" value="{{.}}"><button type="submit">✕</button></form>
  </span>
{{else}}<span>（空）</span>{{end}}
<table>
<tr><th>ID</th><th>Topic</th><th>设备名 / 型号</th><th>状态</th><th>消息数</th><th>实体（可编辑）</th><th>操作</th></tr>
{{range .Devices}}
<tr>
<td>{{.ID}}</td>
<td><code>{{.Topic}}</code></td>
<td>
  <form method="post" action="/api/device/{{.ID}}">
    <input type="text" class="wide" name="name" value="{{.Name}}"><br>
    <input type="text" class="wide" name="model" value="{{.Model}}">
    <input type="hidden" name="manufacturer" value="{{.Manufacturer}}">
    <input type="hidden" name="serial" value="{{.Serial}}">
    <button type="submit">保存</button>
  </form>
</td>
<td class="{{.Status}}">{{.Status}}</td><td>{{.MsgCount}}</td>
<td>
{{range $e := .Entities}}
  <div class="entity-row">
  <form method="post" action="/api/entity/{{$e.ID}}">
    <code>{{$e.Field}}</code>
    <input type="text" name="name" value="{{$e.Name}}">
    <input type="text" name="device_class" value="{{$e.DeviceClass}}" placeholder="class">
    <input type="text" name="unit" value="{{$e.Unit}}" placeholder="unit" size="4">
    <label><input type="checkbox" name="enabled" value="1" {{if $e.Enabled}}checked{{end}}> 启用</label>
    <button type="submit">保存</button>
  </form>
  </div>
{{end}}
</td>
<td>
{{if eq .Status "pending"}}<a class="btn" href="/approve/{{.ID}}">批准</a>
<a class="btn" href="/reject/{{.ID}}">拒绝</a>{{end}}
{{if ne .Status "blacklisted"}}<a class="btn" href="/blacklist/{{.ID}}">拉黑</a>{{end}}
<a class="btn" href="/refresh/{{.ID}}">重发布</a>
</td>
</tr>
{{end}}
</table>
</body></html>`))

type devView struct {
	Device
	Entities []Entity
}

type indexData struct {
	Mode      string
	Blacklist []string
	Devices   []devView
}

func (b *Bridge) handleIndex(w http.ResponseWriter, r *http.Request) {
	devs, err := b.store.ListDevices()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	bl, err := b.store.ListBlacklist()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	data := indexData{Mode: b.cfg.Mode, Blacklist: bl}
	for _, d := range devs {
		ents, err := b.store.ListEntities(d.ID)
		if err != nil {
			continue
		}
		data.Devices = append(data.Devices, devView{Device: d, Entities: ents})
	}
	_ = indexTpl.Execute(w, data)
}

func (b *Bridge) handleAPI(w http.ResponseWriter, r *http.Request) {
	devs, err := b.store.ListDevices()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type apiDev struct {
		Device
		Entities []Entity `json:"entities"`
	}
	out := make([]apiDev, 0, len(devs))
	for _, d := range devs {
		ents, _ := b.store.ListEntities(d.ID)
		out = append(out, apiDev{Device: d, Entities: ents})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (b *Bridge) handleApprove(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	dev, err := b.store.GetDeviceByID(id)
	if err != nil {
		http.Error(w, "device not found", 404)
		return
	}
	if err := b.publishDevice(dev); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = b.store.UpdateDeviceStatus(dev.ID, StatusApproved)
	log.Printf("approved by user: %s", dev.Topic)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (b *Bridge) handleReject(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	dev, err := b.store.GetDeviceByID(id)
	if err != nil {
		http.Error(w, "device not found", 404)
		return
	}
	_ = b.store.DeleteDevice(dev.ID)
	log.Printf("rejected by user: %s", dev.Topic)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (b *Bridge) handleBlacklist(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	dev, err := b.store.GetDeviceByID(id)
	if err != nil {
		http.Error(w, "device not found", 404)
		return
	}
	if err := b.store.AddBlacklist(dev.Prefix); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = b.store.UpdateDeviceStatus(dev.ID, StatusBlacklisted)
	log.Printf("blacklisted by user: %s (prefix %s)", dev.Topic, dev.Prefix)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (b *Bridge) handleRefresh(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	dev, err := b.store.GetDeviceByID(id)
	if err != nil {
		http.Error(w, "device not found", 404)
		return
	}
	if err := b.publishDevice(dev); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("re-published by user: %s", dev.Topic)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---- M2: entity / device editing ----

// handleEntityUpdate updates one entity (name/class/unit/enabled) and
// re-publishes the device discovery so HA reflects the change.
func (b *Bridge) handleEntityUpdate(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	ent, err := b.getEntityByID(id)
	if err != nil {
		http.Error(w, "entity not found", 404)
		return
	}
	ent.Name = r.FormValue("name")
	ent.DeviceClass = r.FormValue("device_class")
	ent.Unit = r.FormValue("unit")
	ent.Icon = r.FormValue("icon")
	ent.Enabled = r.FormValue("enabled") == "1"
	if err := b.store.UpdateEntity(id, ent); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := b.republishDeviceOfEntity(ent.DeviceID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("entity %d updated (enabled=%v)", id, ent.Enabled)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDeviceUpdate updates device metadata and re-publishes discovery.
func (b *Bridge) handleDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	if err := b.store.UpdateDeviceMeta(id,
		r.FormValue("name"), r.FormValue("model"),
		r.FormValue("manufacturer"), r.FormValue("serial")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	dev, err := b.store.GetDeviceByID(id)
	if err != nil {
		http.Error(w, "device not found", 404)
		return
	}
	if err := b.publishDevice(dev); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("device %d metadata updated", id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---- M2: export / import ----

type exportEntity struct {
	Field       string `json:"field"`
	Name        string `json:"name"`
	DeviceClass string `json:"device_class,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type exportDevice struct {
	ID           int64          `json:"id"`
	Topic        string         `json:"topic"`
	Prefix       string         `json:"prefix"`
	Name         string         `json:"name"`
	Manufacturer string         `json:"manufacturer"`
	Model        string         `json:"model"`
	Serial       string         `json:"serial"`
	Status       string         `json:"status"`
	MsgCount     int            `json:"msg_count"`
	Entities     []exportEntity `json:"entities"`
}

type exportData struct {
	Version   int            `json:"version"`
	Devices   []exportDevice `json:"devices"`
	Blacklist []string       `json:"blacklist"`
}

func (b *Bridge) handleExport(w http.ResponseWriter, r *http.Request) {
	devs, err := b.store.ListDevices()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	bl, err := b.store.ListBlacklist()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := exportData{Version: 1, Blacklist: bl}
	for _, d := range devs {
		ents, _ := b.store.ListEntities(d.ID)
		ed := exportDevice{
			ID: d.ID, Topic: d.Topic, Prefix: d.Prefix, Name: d.Name,
			Manufacturer: d.Manufacturer, Model: d.Model, Serial: d.Serial,
			Status: d.Status, MsgCount: d.MsgCount,
		}
		for _, e := range ents {
			ed.Entities = append(ed.Entities, exportEntity{
				Field: e.Field, Name: e.Name, DeviceClass: e.DeviceClass,
				Unit: e.Unit, Icon: e.Icon, Enabled: e.Enabled,
			})
		}
		out.Devices = append(out.Devices, ed)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="mqtt2ha-export.json"`)
	_ = json.NewEncoder(w).Encode(out)
}

func (b *Bridge) handleImport(w http.ResponseWriter, r *http.Request) {
	var data exportData
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&data); err != nil {
		// also accept multipart file upload
		if f, _, ferr := r.FormFile("file"); ferr == nil {
			defer f.Close()
			dec = json.NewDecoder(f)
			if derr := dec.Decode(&data); derr != nil {
				http.Error(w, "bad json: "+derr.Error(), 400)
				return
			}
		} else {
			http.Error(w, "bad json: "+err.Error(), 400)
			return
		}
	}
	if data.Version != 1 {
		http.Error(w, fmt.Sprintf("unsupported export version %d", data.Version), 400)
		return
	}
	devs := make([]Device, 0, len(data.Devices))
	ents := make(map[int64][]Entity, len(data.Devices))
	for _, d := range data.Devices {
		devs = append(devs, Device{
			ID: d.ID, Topic: d.Topic, Prefix: d.Prefix, Name: d.Name,
			Manufacturer: d.Manufacturer, Model: d.Model, Serial: d.Serial,
			Status: d.Status, MsgCount: d.MsgCount,
		})
		for _, e := range d.Entities {
			ents[d.ID] = append(ents[d.ID], Entity{
				Field: e.Field, Name: e.Name, DeviceClass: e.DeviceClass,
				Unit: e.Unit, Icon: e.Icon, Enabled: e.Enabled,
			})
		}
	}
	if err := b.store.ImportSnapshot(devs, ents, data.Blacklist); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("imported %d devices, %d blacklist entries", len(devs), len(data.Blacklist))
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(fmt.Sprintf("imported %d devices, %d blacklist entries", len(devs), len(data.Blacklist))))
}

// ---- M2: blacklist management ----

func (b *Bridge) handleBlacklistDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	prefix := r.FormValue("prefix")
	if prefix == "" {
		http.Error(w, "missing prefix", 400)
		return
	}
	if err := b.store.DeleteBlacklist(prefix); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("blacklist entry removed: %s", prefix)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// getEntityByID fetches an entity by id.
func (b *Bridge) getEntityByID(id int64) (Entity, error) {
	devs, err := b.store.ListDevices()
	if err != nil {
		return Entity{}, err
	}
	for _, d := range devs {
		ents, err := b.store.ListEntities(d.ID)
		if err != nil {
			return Entity{}, err
		}
		for _, e := range ents {
			if e.ID == id {
				return e, nil
			}
		}
	}
	return Entity{}, fmt.Errorf("entity %d not found", id)
}

// republishDeviceOfEntity re-publishes discovery for the device owning an entity.
func (b *Bridge) republishDeviceOfEntity(deviceID int64) error {
	dev, err := b.store.GetDeviceByID(deviceID)
	if err != nil {
		return err
	}
	return b.publishDevice(dev)
}

func idFromPath(w http.ResponseWriter, r *http.Request) int64 {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, "bad path", 400)
		return 0
	}
	// take the LAST segment so it works for both /approve/<id> and /api/entity/<id>
	id, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad id %q", parts[len(parts)-1]), 400)
		return 0
	}
	return id
}
