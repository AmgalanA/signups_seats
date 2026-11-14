// notifier is the service for a separate action: sending sign-up confirmations.
//
// It consumes signup.created and signup.cancelled events from RabbitMQ and
// "sends" a confirmation. In this project sending means writing a log line;
// a real system would call a mail service here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Broker layout, the same in booking and notifier:
//
//	booking --(signup.created / signup.cancelled)--> exchange "signup"
//	                                 --(signup.*)--> queue "notifier.signups" --> notifier
const (
	exchangeName = "signup"
	queueName    = "notifier.signups"
	bindingKey   = "signup.*"

	eventCreated   = "signup.created"
	eventCancelled = "signup.cancelled"
)

// event is the message body, sent by booking as JSON.
type event struct {
	Type     string    `json:"type"`
	SignupID int64     `json:"signup_id"`
	SeatID   int       `json:"seat_id"`
	Row      int       `json:"row"`
	Number   int       `json:"number"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	At       time.Time `json:"at"`
}

func main() {
	addr := ":" + envOr("PORT", "8081")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("notifier: health server failed: %v", err)
		}
	}()
	log.Printf("notifier: started, health check on %s", addr)

	// Consume until the process is asked to stop.
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		consume(ctx, brokerURLs())
	}()

	<-ctx.Done()
	log.Print("notifier: shutting down")
	<-consumerDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("notifier: shutdown error: %v", err)
	}
}

// consume keeps a consumer running: when the connection to the broker breaks
// (a node died, restarted or is not up yet) it waits and connects again.
func consume(ctx context.Context, urls []string) {
	for ctx.Err() == nil {
		err := consumeOnce(ctx, urls)
		if ctx.Err() != nil {
			return
		}
		log.Printf("notifier: no broker connection, will retry: %v", err)
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

// dialAny tries the broker nodes in order and returns a connection to the
// first one that answers. When a node dies, the next attempt lands on another
// node: this is how notifier survives the loss of a broker node.
func dialAny(urls []string) (*amqp.Connection, string, error) {
	var lastErr error
	for _, url := range urls {
		node := "?"
		if uri, err := amqp.ParseURI(url); err == nil {
			node = uri.Host
		}
		conn, err := amqp.DialConfig(url, amqp.Config{
			Heartbeat: 10 * time.Second,
			Dial:      amqp.DefaultDial(3 * time.Second),
		})
		if err == nil {
			return conn, node, nil
		}
		log.Printf("notifier: broker node %s is not available: %v", node, err)
		lastErr = err
	}
	return nil, "", fmt.Errorf("connect: %w", lastErr)
}

// consumeOnce connects, then handles messages until the connection breaks or
// ctx is cancelled.
func consumeOnce(ctx context.Context, urls []string) error {
	conn, node, err := dialAny(urls)
	if err != nil {
		return err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	if err := declareTopology(ch); err != nil {
		return err
	}
	// The broker gives this consumer at most 10 unacknowledged messages at a time.
	if err := ch.Qos(10, 0, false); err != nil {
		return fmt.Errorf("set prefetch: %w", err)
	}
	// autoAck is false: a message leaves the queue only after we acknowledge it.
	// If notifier dies in the middle, the broker delivers the message again.
	deliveries, err := ch.Consume(queueName, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("start consuming: %w", err)
	}
	log.Printf("notifier: connected to broker node %s, waiting for messages in %q", node, queueName)

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("the broker closed the connection")
			}
			handle(d)
		}
	}
}

// handle processes one message and acknowledges it.
//
// Delivery is "at least once": after a failure the same message can arrive
// twice (same MessageId). Here a repeat only writes a second log line; a real
// mail sender would remember the MessageId to avoid a second letter.
func handle(d amqp.Delivery) {
	var e event
	if err := json.Unmarshal(d.Body, &e); err != nil {
		log.Printf("notifier: dropped a message that is not valid JSON: %v", err)
		_ = d.Nack(false, false) // do not put it back: it will never become valid
		return
	}

	switch e.Type {
	case eventCreated:
		log.Printf("notifier: confirmation sent to %s: sign-up %d, row %d seat %d",
			maskEmail(e.Email), e.SignupID, e.Row, e.Number)
	case eventCancelled:
		log.Printf("notifier: cancellation notice sent to %s: sign-up %d, row %d seat %d is free again",
			maskEmail(e.Email), e.SignupID, e.Row, e.Number)
	default:
		log.Printf("notifier: ignored a message of unknown type %q", e.Type)
	}

	if err := d.Ack(false); err != nil {
		log.Printf("notifier: acknowledge failed, the message will come again: %v", err)
	}
}

// declareTopology creates the exchange, the queue and the binding if they do
// not exist yet. Both booking and notifier do this, so it does not matter
// which of them starts first. Everything is durable: it survives a broker restart.
func declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(exchangeName, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}
	// A quorum queue is replicated between broker nodes (used in stage 5).
	// On a single node it works like a normal durable queue.
	args := amqp.Table{"x-queue-type": "quorum"}
	if _, err := ch.QueueDeclare(queueName, true, false, false, false, args); err != nil {
		return fmt.Errorf("declare queue: %w", err)
	}
	if err := ch.QueueBind(queueName, bindingKey, exchangeName, false, nil); err != nil {
		return fmt.Errorf("bind queue: %w", err)
	}
	return nil
}

// maskEmail hides most of the address so that logs do not collect personal
// data: "ivan@example.com" becomes "i***@example.com".
func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 1 {
		return "***"
	}
	first := []rune(email[:at])[0]
	return string(first) + "***" + email[at:]
}

// brokerURLs builds one RabbitMQ address per cluster node from RABBITMQ_*
// environment variables. RABBITMQ_HOSTS is a comma-separated list of nodes.
func brokerURLs() []string {
	port, err := strconv.Atoi(envOr("RABBITMQ_PORT", "5672"))
	if err != nil {
		log.Fatalf("notifier: RABBITMQ_PORT must be a number: %v", err)
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
		log.Fatal("notifier: RABBITMQ_HOSTS is empty")
	}
	return urls
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
