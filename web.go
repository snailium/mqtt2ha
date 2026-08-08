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
}

var indexTpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>mqtt2ha</title>
<style>
body{font-family:sans-serif;max-width:1000px;margin:20px auto;padding:0 10px}
table{border-collapse:collapse;width:100%}
td,th{border:1px solid #ccc;padding:6px 10px;text-align:left}
.pending{color:#b58900}.approved{color:#2e7d32}.blacklisted{color:#c62828}
.entity{font-size:12px;color:#555}
a.btn{margin-right:6px;text-decoration:none;border:1px solid #888;padding:2px 8px;border-radius:3px}
</style></head><body>
<h1>mqtt2ha — 节点管理 (mode: {{.Mode}})</h1>
<h3>黑名单: {{range .Blacklist}}<code>{{.}}</code> {{else}}（空）{{end}}</h3>
<table>
<tr><th>ID</th><th>Topic</th><th>设备名</th><th>型号</th><th>状态</th><th>消息数</th><th>实体</th><th>操作</th></tr>
{{range .Devices}}
<tr>
<td>{{.ID}}</td><td><code>{{.Topic}}</code></td><td>{{.Name}}</td><td>{{.Model}}</td>
<td class="{{.Status}}">{{.Status}}</td><td>{{.MsgCount}}</td>
<td class="entity">{{range $e := .Entities}}{{.Field}}{{if not .Enabled}}✗{{end}} {{end}}</td>
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

func idFromPath(w http.ResponseWriter, r *http.Request) int64 {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		http.Error(w, "bad path", 400)
		return 0
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("bad id %q", parts[1]), 400)
		return 0
	}
	return id
}
