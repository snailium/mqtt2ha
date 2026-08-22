package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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

	// Graceful shutdown (P2 #9): SIGINT/SIGTERM disconnect MQTT, close the
	// store (checkpointing SQLite WAL), then exit cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		log.Printf("shutdown signal received; disconnecting")
		stop()
		close(done)
	}()

	bridge := NewBridge(cfg, store)
	if err := bridge.Start(); err != nil {
		log.Fatalf("start bridge: %v", err)
	}
	log.Printf("mqtt2ha started (mode=%s, observe=%d, broker=%s)", cfg.Mode, cfg.Observe, cfg.MQTT.Broker)

	// Web UI (M2). Empty cfg.HTTP disables the UI entirely.
	if cfg.HTTP != "" {
		mux := http.NewServeMux()
		bridge.RegisterWeb(mux)
		srv := &http.Server{Addr: cfg.HTTP, Handler: mux}
		go func() {
			log.Printf("web UI listening on %s", cfg.HTTP)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("web server: %v (bridge keeps running)", err)
			}
		}()
		go func() {
			<-ctx.Done()
			_ = srv.Shutdown(context.Background())
		}()
	} else {
		log.Printf("web UI disabled (http is empty)")
	}

	<-done
	log.Printf("mqtt2ha exited")
}
