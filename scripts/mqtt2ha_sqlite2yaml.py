#!/usr/bin/env python3
"""mqtt2ha_sqlite2yaml.py — 一次性迁移：SQLite -> devices/*.yaml
读 mqtt2ha.db（devices/entities/blacklist 表），写出 yaml backend 格式。
用法: python3 mqtt2ha_sqlite2yaml.py /path/to/mqtt2ha.db /path/to/devices_dir
"""
import sqlite3, sys, os, yaml

def main():
    db_path = sys.argv[1] if len(sys.argv) > 1 else "mqtt2ha.db"
    out_dir = sys.argv[2] if len(sys.argv) > 2 else "devices"
    os.makedirs(out_dir, exist_ok=True)

    db = sqlite3.connect(db_path)
    db.row_factory = sqlite3.Row

    devs = db.execute("SELECT * FROM devices ORDER BY id").fetchall()
    ents = db.execute("SELECT * FROM entities ORDER BY device_id, id").fetchall()
    bl = db.execute("SELECT * FROM blacklist").fetchall()

    by_dev = {}
    for e in ents:
        by_dev.setdefault(e["device_id"], []).append(e)

    def sanitize(topic):
        for ch in "/\\ #+*":
            topic = topic.replace(ch, "_")
        return topic

    n = 0
    for d in devs:
        doc = {
            "id": d["id"],
            "topic": d["topic"],
            "status": d["status"],
            "name": d["name"] or "",
            "manufacturer": d["manufacturer"] or "",
            "model": d["model"] or "",
            "serial": d["serial"] or "",
            "entities": [],
        }
        for e in by_dev.get(d["id"], []):
            doc["entities"].append({
                "field": e["field"],
                "name": e["name"] or "",
                "component": e["component"] or "sensor",
                "device_class": e["device_class"] or "",
                "unit": e["unit"] or "",
                "icon": e["icon"] or "",
                "enabled": bool(e["enabled"]),
            })
        path = os.path.join(out_dir, sanitize(d["topic"]) + ".yaml")
        with open(path, "w") as f:
            yaml.safe_dump(doc, f, allow_unicode=True, sort_keys=False, default_flow_style=False)
        n += 1
        print(f"  {path}  ({len(doc['entities'])} entities, status={d['status']})")

    if bl:
        with open(os.path.join(out_dir, "blacklist.yaml"), "w") as f:
            yaml.safe_dump({"blacklist": [b["prefix"] for b in bl]}, f, allow_unicode=True, sort_keys=False)
        print(f"  {os.path.join(out_dir, 'blacklist.yaml')}  ({len(bl)} prefixes)")

    print(f"\n迁移完成: {n} 个设备 -> {out_dir}/")
    print("提示: 备份原 db 后再切换 backend: \"yaml\"")

if __name__ == "__main__":
    main()
