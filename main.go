package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	Port string
	DSN  string
}

func loadConfig() Config {
	port := getEnvOrDefault("APP_PORT", "8080")

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		getEnvOrDefault("DB_USER", "postgres"),
		getEnvOrDefault("DB_PASSWORD", "postgres"),
		getEnvOrDefault("DB_HOST", "localhost"),
		getEnvOrDefault("DB_PORT", "5432"),
		getEnvOrDefault("DB_NAME", "distsys"),
	)

	return Config{Port: port, DSN: dsn}
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func connectWithRetry(dsn string, maxAttempts int) (*sql.DB, error) {
	var db *sql.DB
	var err error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		db, err = sql.Open("pgx", dsn)
		if err != nil {
			log.Printf("attempt %d:%d: sql.Open failed: %v", attempt, maxAttempts, err)
			sleepwithBackoff(attempt)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := db.PingContext(ctx)
		cancel()

		if err == nil {
			log.Printf("connected to database on attempt %d", attempt)
			return db, nil
		}
		log.Printf("attempt %d:%d: ping failed: %v", attempt, maxAttempts, err)
		db.Close()
		sleepwithBackoff(attempt)
	}

	return nil, fmt.Errorf("could not connect after %d attempts: %w", maxAttempts, err)
}

func sleepwithBackoff(attempt int) {
	base := math.Pow(2, float64(attempt-1))

	if base > 30 {
		base = 30
	}

	jitter := rand.Float64()

	wait := time.Duration((base+jitter)*1000) * time.Millisecond
	log.Printf("waiting %v before next attempt...", wait.Round(time.Millisecond))
	time.Sleep(wait)

}

func runMigrations(ctx context.Context, db *sql.DB) error {
	migration, err := os.ReadFile("migrations/001_create_events.sql")
	if err != nil {
		return fmt.Errorf("could not read migration file: %w", err)
	}

	_, err = db.ExecContext(ctx, string(migration))
	if err != nil {
		return fmt.Errorf("could not run migration: %w", err)
	}
	log.Println("migrations applied successfully")
	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

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

	db, err := connectWithRetry(cfg.DSN, 10)
	if err != nil {
		log.Fatalf("fatal: could not connect to database: %v", err)
	}
	defer db.Close()

	if err := runMigrations(context.Background(), db); err != nil {
		log.Fatalf("fatal: %v", err)
	}

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

	go func() {
		log.Printf("server starting on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutdown signal received, draining connections...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}

	log.Println("server stopped cleanly")

}
