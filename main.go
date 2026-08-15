package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Printf("loading config: %v", err)
		cfg = DefaultConfig()
	}

	var store Backend
	if cfg.Backend == "yaml" {
		st, err := NewYamlStore(cfg.DevicesDir)
		if err != nil {
			log.Fatalf("open yaml store: %v", err)
		}
		store = st
		log.Printf("backend: yaml (devices_dir=%s)", cfg.DevicesDir)
	} else {
		st, err := OpenStore(cfg.DBPath)
		if err != nil {
			log.Fatalf("open store: %v", err)
		}
		store = st
	}
	defer store.Close()

	bridge := NewBridge(cfg, store)
	if err := bridge.Start(); err != nil {
		log.Fatalf("start bridge: %v", err)
	}
	log.Printf("mqtt2ha started (mode=%s, observe=%d, broker=%s)", cfg.Mode, cfg.Observe, cfg.MQTT.Broker)

	// Web UI (M2)
	mux := http.NewServeMux()
	bridge.RegisterWeb(mux)
	go func() {
		log.Printf("web UI listening on %s", cfg.HTTP)
		if err := http.ListenAndServe(cfg.HTTP, mux); err != nil {
			log.Printf("web server: %v (bridge keeps running)", err)
		}
	}()

	select {}
}
