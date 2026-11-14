// booking is the back-end service of the seat sign-up app.
//
// Files:
//
//	main.go     start-up, routes, shutdown
//	handlers.go HTTP handlers and input checks
//	store.go    PostgreSQL: seats and sign-ups (select, insert, update, delete)
//	cache.go    Redis: cached seat map
//	events.go   RabbitMQ: publishing signup.created / signup.cancelled
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func main() {
	// Stop cleanly on Ctrl+C or `docker stop`.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Database: settings come from the PG* environment variables.
	db, err := openStore(ctx)
	if err != nil {
		log.Fatalf("booking: database: %v", err)
	}
	defer db.Close()
	log.Print("booking: connected to the database")

	// Cache and broker connect in the background and reconnect by themselves,
	// so booking starts and serves requests even when they are down.
	cache := openCache(envOr("REDIS_ADDR", "localhost:6379"), os.Getenv("REDIS_PASSWORD"))
	defer cache.Close()

	events := startPublisher(brokerURLs())

	a := &api{store: db, cache: cache, events: events}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hello", hello)
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /api/seats", a.listSeats)
	mux.HandleFunc("POST /api/signups", a.createSignup)
	mux.HandleFunc("DELETE /api/signups/{id}", a.cancelSignup)

	addr := ":" + envOr("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           logRequests(withTimeout(5*time.Second, mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	go func() {
		log.Printf("booking: listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("booking: server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("booking: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("booking: shutdown error: %v", err)
	}
	// The server no longer accepts requests; let waiting events go out.
	events.Close(3 * time.Second)
}

func hello(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "booking",
		"message": "Hello, world!",
	})
}

func health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// brokerURLs builds one RabbitMQ address per cluster node from RABBITMQ_*
// environment variables. RABBITMQ_HOSTS is a comma-separated list of nodes;
// booking uses the first one that answers.
func brokerURLs() []string {
	port, err := strconv.Atoi(envOr("RABBITMQ_PORT", "5672"))
	if err != nil {
		log.Fatalf("booking: RABBITMQ_PORT must be a number: %v", err)
	}
	var urls []string
	for _, host := range strings.Split(envOr("RABBITMQ_HOSTS", "localhost"), ",") {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		urls = append(urls, amqp.URI{
			Scheme:   "amqp",
			Host:     host,
			Port:     port,
			Username: envOr("RABBITMQ_USER", "guest"),
			Password: envOr("RABBITMQ_PASSWORD", "guest"),
			Vhost:    "/",
		}.String())
	}
	if len(urls) == 0 {
		log.Fatal("booking: RABBITMQ_HOSTS is empty")
	}
	return urls
}

// withTimeout cancels database and cache work of a request after d,
// so one stuck query cannot hold a connection forever.
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder remembers the status code so that it can be logged.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("booking: %s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
