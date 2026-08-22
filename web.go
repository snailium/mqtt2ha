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

// RegisterWeb wires the M2 web UI routes. Every route is guarded by optional
// bearer-token auth (config.WebToken); state-changing routes additionally
// require POST + a valid CSRF token.
func (b *Bridge) RegisterWeb(mux *http.ServeMux) {
	// All routes pass through auth. Handler names registered as both the bare
	// path and (where needed) with a trailing segment.
	mux.HandleFunc("/", b.requireAuth(b.handleIndex))
	mux.HandleFunc("/api/devices", b.requireAuth(b.handleAPI))
	mux.HandleFunc("/api/reload", b.requireAuth(b.handleReload))
	mux.HandleFunc("/approve/", b.requireAuth(b.handleApprove))
	mux.HandleFunc("/reject/", b.requireAuth(b.handleReject))
	mux.HandleFunc("/blacklist/", b.requireAuth(b.handleBlacklist))
	mux.HandleFunc("/refresh/", b.requireAuth(b.handleRefresh))
	// M2: edit + export/import
	mux.HandleFunc("/api/entity/", b.requireAuth(b.handleEntityUpdate))
	mux.HandleFunc("/api/device/", b.requireAuth(b.handleDeviceUpdate))
	mux.HandleFunc("/api/export", b.requireAuth(b.handleExport))
	mux.HandleFunc("/api/import", b.requireAuth(b.handleImport))
	mux.HandleFunc("/api/blacklist/delete", b.requireAuth(b.handleBlacklistDelete))
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
    <input type="hidden" name="csrf" value="{{$.CSRF}}">
    <input type="file" name="file" accept=".json" required>
    <button type="submit">导入配置</button>
  </form>
</div>
<h3>黑名单:</h3>
{{range .Blacklist}}
  <span class="bl-item"><code>{{.}}</code>
  <form class="inline" method="post" action="/api/blacklist/delete"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="prefix" value="{{.}}"><button type="submit">✕</button></form>
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
    <input type="hidden" name="csrf" value="{{$.CSRF}}">
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
    <input type="hidden" name="csrf" value="{{$.CSRF}}">
    <code>{{$e.Field}}</code>
    <select name="component">
      <option value="sensor" {{if eq $e.Component "sensor"}}selected{{end}}>sensor</option>
      <option value="binary_sensor" {{if eq $e.Component "binary_sensor"}}selected{{end}}>binary</option>
    </select>
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
{{if eq .Status "pending"}}<form class="inline" method="post" action="/approve/{{.ID}}"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit" class="btn">批准</button></form>
<form class="inline" method="post" action="/reject/{{.ID}}"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit" class="btn">拒绝</button></form>{{end}}
{{if ne .Status "blacklisted"}}<form class="inline" method="post" action="/blacklist/{{.ID}}"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit" class="btn">拉黑 {{.Topic}}</button></form>{{end}}
<form class="inline" method="post" action="/refresh/{{.ID}}"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit" class="btn">重发布</button></form>
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
	CSRF      string
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
	data := indexData{Mode: b.cfg.Mode, CSRF: b.csrfToken, Blacklist: bl}
	for _, d := range devs {
		ents, err := b.store.ListEntities(d.ID)
		if err != nil {
			continue
		}
		data.Devices = append(data.Devices, devView{Device: d, Entities: ents})
	}
	_ = indexTpl.Execute(w, data)
}

// handleReload manually re-reads all device yaml files and re-publishes
// discovery for any whose entity set changed ("update instead of delete").
// Trigger after editing files in devices_dir.
func (b *Bridge) handleReload(w http.ResponseWriter, r *http.Request) {
	if !b.requireWrite(w, r) {
		return
	}
	n, err := b.ReloadAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"reloaded": n})
}

// ReloadAll re-reads every device yaml file and re-publishes discovery for any
// whose entity set changed. Returns the number of discovery sets re-published.
func (b *Bridge) ReloadAll() (int, error) {
	devs, err := b.store.ListDevices()
	if err != nil {
		return 0, err
	}
	republished := 0
	for idx := range devs {
		d := &devs[idx]
		changed, err := b.store.ReloadDevice(d.Topic)
		if err != nil {
			log.Printf("reload %s: %v", d.Topic, err)
			continue
		}
		if !changed {
			continue
		}
		if d.Status != StatusApproved {
			log.Printf("reloaded %s (status=%s, skipped publish)", d.Topic, d.Status)
			continue
		}
		if err := b.publishDevice(d); err != nil {
			log.Printf("reload re-publish %s: %v", d.Topic, err)
		} else {
			republished++
			log.Printf("/api/reload re-published discovery: %s", d.Topic)
		}
	}
	return republished, nil
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
	if !b.requireWrite(w, r) {
		return
	}
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
	if !b.requireWrite(w, r) {
		return
	}
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
	if !b.requireWrite(w, r) {
		return
	}
	id := idFromPath(w, r)
	if id == 0 {
		return
	}
	dev, err := b.store.GetDeviceByID(id)
	if err != nil {
		http.Error(w, "device not found", 404)
		return
	}
	// P1 #7: blacklist the EXACT topic, not the first-segment prefix. This
	// matches README's "exact-topic blacklist" claim and avoids nuking
	// unrelated topics under the same first segment (e.g. home/ups/cp1000).
	if err := b.store.AddBlacklist(dev.Topic); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = b.store.UpdateDeviceStatus(dev.ID, StatusBlacklisted)
	log.Printf("blacklisted by user: %s (exact topic)", dev.Topic)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (b *Bridge) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !b.requireWrite(w, r) {
		return
	}
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
	if !b.requireWrite(w, r) {
		return
	}
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
	ent.Component = r.FormValue("component")
	if ent.Component == "" {
		ent.Component = ComponentSensor
	}
	// P1 #4: only allow whitelisted component names (prevents the "component" field
	// from smuggling arbitrary HA component identifiers into discovery).
	if !validComponent(ent.Component) {
		http.Error(w, "unsupported component: "+ent.Component, 400)
		return
	}
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
	if !b.requireWrite(w, r) {
		return
	}
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
	Component   string `json:"component,omitempty"`
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
				Field: e.Field, Name: e.Name, Component: e.Component,
				DeviceClass: e.DeviceClass,
				Unit:        e.Unit, Icon: e.Icon, Enabled: e.Enabled,
			})
		}
		out.Devices = append(out.Devices, ed)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="mqtt2ha-export.json"`)
	_ = json.NewEncoder(w).Encode(out)
}

func (b *Bridge) handleImport(w http.ResponseWriter, r *http.Request) {
	// P1 #4: import replaces the whole registry — require a valid write CSRF.
	if !b.requireWrite(w, r) {
		return
	}
	// P1 #4: cap the request body to avoid memory-exhaustion DoS via an
	// unbounded import body.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	var data exportData
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&data); err != nil {
		// also accept multipart file upload
		if f, _, ferr := r.FormFile("file"); ferr == nil {
			defer f.Close()
			// protect the multipart upload too
			dec = json.NewDecoder(http.MaxBytesReader(w, f, 1<<20))
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
	// P1 #4: validate imported rows — non-empty topic, whitelisted component,
	// known status; reject anything malformed before it touches the registry.
	validStatus := map[string]bool{StatusPending: true, StatusApproved: true, StatusBlacklisted: true}
	for _, d := range data.Devices {
		if trimBlank(d.Topic) {
			http.Error(w, "import rejected: device with empty topic", 400)
			return
		}
		if d.Status != "" && !validStatus[d.Status] {
			http.Error(w, fmt.Sprintf("import rejected: invalid status %q", d.Status), 400)
			return
		}
		for _, e := range d.Entities {
			if trimBlank(e.Field) {
				http.Error(w, "import rejected: entity with empty field", 400)
				return
			}
			if e.Component != "" && !validComponent(e.Component) {
				http.Error(w, fmt.Sprintf("import rejected: unsupported component %q", e.Component), 400)
				return
			}
		}
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
				Field: e.Field, Name: e.Name, Component: e.Component,
				DeviceClass: e.DeviceClass,
				Unit:        e.Unit, Icon: e.Icon, Enabled: e.Enabled,
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
	if !b.requireWrite(w, r) {
		return
	}
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
