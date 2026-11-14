CREATE TABLE IF NOT EXISTS seats (
    id      integer  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    row_no  smallint NOT NULL CHECK (row_no > 0),
    seat_no smallint NOT NULL CHECK (seat_no > 0),
    status  text     NOT NULL DEFAULT 'free' CHECK (status IN ('free', 'taken')),
    UNIQUE (row_no, seat_no)
);

CREATE TABLE IF NOT EXISTS signups (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    seat_id           integer     NOT NULL UNIQUE REFERENCES seats (id),
    name              text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    email             text        NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
    cancel_token_hash text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);

INSERT INTO seats (row_no, seat_no)
SELECT r, s
FROM generate_series(1, 5) AS r, generate_series(1, 8) AS s
ON CONFLICT (row_no, seat_no) DO NOTHING;
