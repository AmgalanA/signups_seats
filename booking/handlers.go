package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// api holds what the request handlers need.
type api struct {
	store  *store
	cache  *seatCache
	events *publisher
}

// GET /api/seats: the seat map. Reads the cache first, then the database.
// The X-Cache response header shows where the answer came from.
func (a *api) listSeats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if seats, ok := a.cache.Get(ctx); ok {
		w.Header().Set("X-Cache", "HIT")
		writeJSON(w, http.StatusOK, map[string]any{"seats": seats})
		return
	}

	seats, err := a.store.ListSeats(ctx)
	if err != nil {
		serverError(w, "list seats", err)
		return
	}
	a.cache.Set(ctx, seats)

	w.Header().Set("X-Cache", "MISS")
	writeJSON(w, http.StatusOK, map[string]any{"seats": seats})
}

// POST /api/signups: take a seat.
// Body: {"seat_id": 1, "name": "...", "email": "..."}.
// The answer contains cancel_token, the secret needed to cancel later.
func (a *api) createSignup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SeatID int    `json:"seat_id"`
		Name   string `json:"name"`
		Email  string `json:"email"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be JSON with seat_id, name and email")
		return
	}

	name := strings.TrimSpace(in.Name)
	email := strings.TrimSpace(in.Email)
	if in.SeatID <= 0 {
		writeError(w, http.StatusBadRequest, "seat_id must be a positive number")
		return
	}
	if !validName(name) {
		writeError(w, http.StatusBadRequest, "name must be 1 to 100 characters")
		return
	}
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "email is not a valid address")
		return
	}

	token, err := newToken()
	if err != nil {
		serverError(w, "generate token", err)
		return
	}

	su, err := a.store.CreateSignup(r.Context(), in.SeatID, name, email, hashToken(token))
	switch {
	case errors.Is(err, errSeatNotFound):
		writeError(w, http.StatusNotFound, "there is no such seat")
		return
	case errors.Is(err, errSeatTaken):
		writeError(w, http.StatusConflict, "this seat is already taken")
		return
	case err != nil:
		serverError(w, "create sign-up", err)
		return
	}

	a.afterChange(r.Context(), eventCreated, su)

	writeJSON(w, http.StatusCreated, struct {
		signup
		CancelToken string `json:"cancel_token"`
	}{su, token})
}

// DELETE /api/signups/{id}: cancel a sign-up and free the seat.
// The X-Cancel-Token header must carry the token from the sign-up answer.
func (a *api) cancelSignup(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "the sign-up id must be a positive number")
		return
	}
	token := r.Header.Get("X-Cancel-Token")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "the X-Cancel-Token header is required")
		return
	}

	su, err := a.store.CancelSignup(r.Context(), id, token)
	switch {
	case errors.Is(err, errSignupNotFound):
		writeError(w, http.StatusNotFound, "there is no such sign-up")
		return
	case errors.Is(err, errWrongToken):
		writeError(w, http.StatusForbidden, "wrong cancel token")
		return
	case err != nil:
		serverError(w, "cancel sign-up", err)
		return
	}

	a.afterChange(r.Context(), eventCancelled, su)
	w.WriteHeader(http.StatusNoContent)
}

// afterChange runs once the database transaction is committed: it drops the
// cached seat map and queues an event for the broker.
func (a *api) afterChange(ctx context.Context, eventType string, su signup) {
	// The change is already saved, so finish this work even if the client
	// has closed the connection in the meantime.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	a.cache.Invalidate(ctx)
	a.events.Publish(event{
		Type:     eventType,
		SignupID: su.ID,
		SeatID:   su.SeatID,
		Row:      su.Row,
		Number:   su.Number,
		Name:     su.Name,
		Email:    su.Email,
		At:       time.Now().UTC(),
	})
}

func validName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 100 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validEmail(email string) bool {
	if len(email) > 254 {
		return false
	}
	addr, err := mail.ParseAddress(email)
	// ParseAddress also accepts "Name <a@b.c>"; require the bare address.
	return err == nil && addr.Address == email
}

// newToken returns 32 random URL-safe characters (192 bits).
func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("booking: write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// serverError logs the real cause and tells the client nothing about it.
func serverError(w http.ResponseWriter, what string, err error) {
	log.Printf("booking: %s: %v", what, err)
	writeError(w, http.StatusInternalServerError, "internal error, try again later")
}
