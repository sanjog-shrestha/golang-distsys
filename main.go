package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Config holds runtime configuration, sourced from environment variables. In distributed systems, config almost never lives in code — it comes from the environment (Docker, Kubernetes, etc.) so the same binary behaves differently in dev/staging/prod without a rebuild.
type Config struct {
	Port string
	DSN  string
}

func loadConfig() Config {
	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}

	// New: build a Postgres connection string from env vars, with local defaults so this still runs even outside Docker.
	host := getEnvOrDefault("DB_HOST", "localhost")
	dbPort := getEnvOrDefault("DB_PORT", "5432")
	user := getEnvOrDefault("DB_USER", "postgres")
	pass := getEnvOrDefault("DB_PASSWORD", "postgres")
	name := getEnvOrDefault("DB_NAME", "distsys")

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, dbPort, name)

	return Config{Port: port, DSN: dsn}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// dbCheckHandler is new. It just asks Postgres "are you there?" and reports back. Nothing else. This is the smallest possible proof that our Go app and the Postgres container can talk to each other.
func dbCheckHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		if err := db.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"db_status": "unreachable",
				"error":     err.Error(),
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"db_status": "connected",
		})
	}
}

func main() {
	cfg := loadConfig()

	// open a connection pool to Postgres. sql.Open doesn't actually connect yet — it just prepares the pool. The real connection attempt happens on first use (like our Ping below).
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/db-check", dbCheckHandler(db))

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a separate goroutine so main() stays free to listen for shutdown signals. This is the core pattern you'll reuse for every long-running Go service: server (or worker/consumer) in a goroutine, signal handling in main.
	go func() {
		log.Printf("server starting on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	// Block until we receive SIGINT (Ctrl+C) or SIGTERM (what Docker/K8s send when stopping a container). This is what makes "graceful shutdown" possible instead of connections being killed mid-flight.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutdown signal received, draining connections...")

	// Give in-flight requests up to 10 seconds to finish before we force-kill.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}

	log.Println("server stopped cleanly")

}
