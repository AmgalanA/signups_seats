package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	seatsKey = "seats:all"
	// Even if an invalidation is lost, a stale seat map lives at most this long.
	// A stale map cannot break the "one seat, one person" rule: that rule is
	// checked in PostgreSQL, the cache only saves reads.
	seatsTTL = 30 * time.Second
)

// seatCache keeps the seat map in Redis (cache-aside): read the cache first,
// on a miss read the database and put the result into the cache, and delete
// the key whenever a seat changes.
//
// Redis is optional for correctness. Every error here is logged and treated
// as a miss, so the app keeps working, only slower, when Redis is down.
type seatCache struct {
	rdb *redis.Client
}

func openCache(addr, password string) *seatCache {
	return &seatCache{rdb: redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DialTimeout:  time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})}
}

func (c *seatCache) Close() { _ = c.rdb.Close() }

// Get reads the seat map from the cache. ok is false on a miss.
func (c *seatCache) Get(ctx context.Context) (seats []seat, ok bool) {
	raw, err := c.rdb.Get(ctx, seatsKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false // plain miss
	}
	if err != nil {
		log.Printf("booking: cache read failed, using the database: %v", err)
		return nil, false
	}
	if err := json.Unmarshal(raw, &seats); err != nil {
		log.Printf("booking: cache holds bad data, using the database: %v", err)
		return nil, false
	}
	return seats, true
}

// Set writes the seat map to the cache.
func (c *seatCache) Set(ctx context.Context, seats []seat) {
	raw, err := json.Marshal(seats)
	if err != nil {
		log.Printf("booking: cache encode failed: %v", err)
		return
	}
	if err := c.rdb.Set(ctx, seatsKey, raw, seatsTTL).Err(); err != nil {
		log.Printf("booking: cache write failed: %v", err)
	}
}

// Invalidate deletes the cached seat map after a seat changed.
func (c *seatCache) Invalidate(ctx context.Context) {
	if err := c.rdb.Del(ctx, seatsKey).Err(); err != nil {
		log.Printf("booking: cache invalidation failed (stale for up to %s): %v", seatsTTL, err)
	}
}
