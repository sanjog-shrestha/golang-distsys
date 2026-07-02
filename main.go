package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Config holds runtime configuration, sourced from environment variables. In distributed systems, config almost never lives in code — it comes from the environment (Docker, Kubernetes, etc.) so the same binary behaves differently in dev/staging/prod without a rebuild.

type Config struct {
	Port string
}

func loadConfig() Config {
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}
	return Config{Port: port}
}

// healthHandler is the endpoint orchestrators (Docker, Kubernetes, load balancers) will hit to ask "are you alive and ready to work?" This single endpoint becomes critical once we have multiple replicas.

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// echoHandler is just so we have a second route to prove routing works.
func echoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "hello from distsys-go",
	})
}

func main() {
	cfg := loadConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/echo", echoHandler)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a separate goroutine so main() stays free to listen for shutdown signals. This is the core pattern you'll reuse for every long-running Go service: server (or worker/consumer) in a goroutine, signal handling in main.
	go func() {
		log.Printf("server starting on prt %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	// Block until we receive SIGINT (Ctrl+C) or SIGTERM (what Docker/K8s send when stopping a container). This is what makes "graceful shutdown" possible instead of connections being killed mid-flight.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutdown signal recieved, draining connections...")

	// Give in-flight requests up to 10 seconds to finish before we force-kill.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}

	log.Println("server stopped cleanly")

}
