package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// event is the message body, sent as JSON.
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

// publisher sends events to RabbitMQ from one background goroutine.
//
// A request handler only puts the event into an in-memory queue and answers
// the user at once, so a slow or dead broker never slows a sign-up down.
// The goroutine keeps retrying until the broker confirms the message, which
// means events written while the broker is down are delivered when it is back.
//
// Limit to know about: the queue is in memory. If booking itself dies with
// events still waiting, they are lost (the sign-up stays in the database, only
// the confirmation is missing). The full fix is the "transactional outbox"
// pattern: write the event to a table in the same transaction as the sign-up.
type publisher struct {
	urls   []string // one address per broker node
	queue  chan event
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

func startPublisher(urls []string) *publisher {
	ctx, cancel := context.WithCancel(context.Background())
	p := &publisher{
		urls:   urls,
		queue:  make(chan event, 1000),
		done:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
	}
	go p.run()
	return p
}

// Publish queues an event and returns at once.
func (p *publisher) Publish(e event) {
	select {
	case p.queue <- e:
	default:
		log.Printf("booking: event queue is full, dropped %s for sign-up %d", e.Type, e.SignupID)
	}
}

// Close gives waiting events up to grace to be sent, then stops.
// Call it only after the HTTP server has stopped, so nothing publishes any more.
func (p *publisher) Close(grace time.Duration) {
	close(p.queue)
	select {
	case <-p.done:
	case <-time.After(grace):
		p.cancel()
		<-p.done
	}
}

func (p *publisher) run() {
	defer close(p.done)

	var conn *amqp.Connection
	var ch *amqp.Channel
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	for e := range p.queue {
		for {
			if p.ctx.Err() != nil {
				log.Printf("booking: shutting down, dropped %s for sign-up %d", e.Type, e.SignupID)
				break
			}
			if ch == nil {
				var err error
				conn, ch, err = connectBroker(p.urls)
				if err != nil {
					log.Printf("booking: no broker node available, will retry: %v", err)
					p.wait(2 * time.Second)
					continue
				}
			}
			err := send(p.ctx, ch, e)
			if err == nil {
				log.Printf("booking: published %s for sign-up %d", e.Type, e.SignupID)
				break
			}
			log.Printf("booking: publish failed, will reconnect: %v", err)
			_ = conn.Close()
			conn, ch = nil, nil
			p.wait(2 * time.Second)
		}
	}
}

func (p *publisher) wait(d time.Duration) {
	select {
	case <-p.ctx.Done():
	case <-time.After(d):
	}
}

// connectBroker tries the broker nodes in order and uses the first one that
// works. When a node dies, the next call simply lands on another node: this
// is how booking survives the loss of a broker node.
func connectBroker(urls []string) (*amqp.Connection, *amqp.Channel, error) {
	var lastErr error
	for _, url := range urls {
		conn, ch, err := connectNode(url)
		if err == nil {
			log.Printf("booking: connected to broker node %s", nodeName(url))
			return conn, ch, nil
		}
		log.Printf("booking: broker node %s is not available: %v", nodeName(url), err)
		lastErr = err
	}
	return nil, nil, lastErr
}

// nodeName returns the host part of a broker address, for log lines
// (the full address contains the password).
func nodeName(url string) string {
	uri, err := amqp.ParseURI(url)
	if err != nil {
		return "?"
	}
	return uri.Host
}

// connectNode opens a connection and a channel to one node, makes sure the
// exchange and queue exist, and switches the channel to "publisher confirms" mode.
func connectNode(url string) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.DialConfig(url, amqp.Config{
		Heartbeat: 10 * time.Second,
		Dial:      amqp.DefaultDial(3 * time.Second),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("open channel: %w", err)
	}
	if err := declareTopology(ch); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	// With confirms on, the broker acknowledges every message it has stored.
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("enable confirms: %w", err)
	}
	return conn, ch, nil
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

// send publishes one event and waits until the broker confirms it.
func send(ctx context.Context, ch *amqp.Channel, e event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	confirmation, err := ch.PublishWithDeferredConfirmWithContext(ctx,
		exchangeName, e.Type, false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent, // written to disk by the broker
			// The same event always has the same id, so a consumer can
			// recognise a message that was delivered twice.
			MessageId: fmt.Sprintf("%s:%d", e.Type, e.SignupID),
			Timestamp: e.At,
			Body:      body,
		})
	if err != nil {
		return err
	}
	acked, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !acked {
		return errors.New("broker did not accept the message")
	}
	return nil
}
