package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors that the HTTP layer turns into status codes.
var (
	errSeatNotFound   = errors.New("seat not found")
	errSeatTaken      = errors.New("seat is already taken")
	errSignupNotFound = errors.New("sign-up not found")
	errWrongToken     = errors.New("wrong cancel token")
)

type seat struct {
	ID     int    `json:"id"`
	Row    int    `json:"row"`
	Number int    `json:"number"`
	Status string `json:"status"`
}

type signup struct {
	ID        int64     `json:"id"`
	SeatID    int       `json:"seat_id"`
	Row       int       `json:"row"`
	Number    int       `json:"number"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// store is everything booking does with PostgreSQL.
type store struct {
	pool *pgxpool.Pool
}

// openStore connects to PostgreSQL. The address, user, password and database
// come from the standard PG* environment variables (PGHOST, PGUSER, ...),
// which pgx reads by itself when the connection string is empty.
func openStore(ctx context.Context) (*store, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("read PG* settings: %w", err)
	}
	cfg.MaxConns = 10

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// The database may still be starting: try for about 30 seconds.
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return &store{pool: pool}, nil
		}
		if attempt == 10 || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("ping database: %w", err)
		}
		time.Sleep(3 * time.Second)
	}
}

func (s *store) Close() { s.pool.Close() }

// ListSeats is the SELECT: the whole seat map.
func (s *store) ListSeats(ctx context.Context) ([]seat, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, row_no, seat_no, status FROM seats ORDER BY row_no, seat_no`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (seat, error) {
		var st seat
		err := row.Scan(&st.ID, &st.Row, &st.Number, &st.Status)
		return st, err
	})
}

// CreateSignup is the UPDATE + INSERT, in one transaction.
//
// The rule "one seat, one person" lives in the UPDATE: it changes the row only
// if the seat is still free. When two people ask for the same seat at the same
// moment, PostgreSQL makes the second UPDATE wait for the first transaction,
// then sees status = 'taken' and changes nothing, so the second person gets
// errSeatTaken.
func (s *store) CreateSignup(ctx context.Context, seatID int, name, email, tokenHash string) (signup, error) {
	su := signup{SeatID: seatID, Name: name, Email: email}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return su, err
	}
	defer tx.Rollback(ctx) // does nothing after a successful Commit

	err = tx.QueryRow(ctx,
		`UPDATE seats SET status = 'taken'
		 WHERE id = $1 AND status = 'free'
		 RETURNING row_no, seat_no`, seatID).Scan(&su.Row, &su.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing changed: either there is no such seat or it is taken.
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM seats WHERE id = $1)`, seatID).Scan(&exists); err != nil {
			return su, err
		}
		if !exists {
			return su, errSeatNotFound
		}
		return su, errSeatTaken
	}
	if err != nil {
		return su, err
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO signups (seat_id, name, email, cancel_token_hash)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`, seatID, name, email, tokenHash).Scan(&su.ID, &su.CreatedAt)
	if err != nil {
		// 23505 = unique_violation: the UNIQUE (seat_id) backstop fired.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return su, errSeatTaken
		}
		return su, err
	}

	return su, tx.Commit(ctx)
}

// CancelSignup is the DELETE + UPDATE, in one transaction. The caller must
// show the secret token that was returned when the sign-up was created.
func (s *store) CancelSignup(ctx context.Context, id int64, token string) (signup, error) {
	su := signup{ID: id}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return su, err
	}
	defer tx.Rollback(ctx)

	var wantHash string
	err = tx.QueryRow(ctx,
		`SELECT seat_id, name, email, created_at, cancel_token_hash
		 FROM signups WHERE id = $1
		 FOR UPDATE`, id).Scan(&su.SeatID, &su.Name, &su.Email, &su.CreatedAt, &wantHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return su, errSignupNotFound
	}
	if err != nil {
		return su, err
	}
	// Constant-time comparison: the time it takes does not reveal how many
	// characters of the token were right.
	if subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(wantHash)) != 1 {
		return su, errWrongToken
	}

	if _, err := tx.Exec(ctx, `DELETE FROM signups WHERE id = $1`, id); err != nil {
		return su, err
	}
	err = tx.QueryRow(ctx,
		`UPDATE seats SET status = 'free' WHERE id = $1
		 RETURNING row_no, seat_no`, su.SeatID).Scan(&su.Row, &su.Number)
	if err != nil {
		return su, err
	}

	return su, tx.Commit(ctx)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
