-- How long Azir keeps the two things that go away on their own.
--
-- Both were constants in Go, and both are shown to somebody on the Data screen
-- as a promise: "goes after 14 days, unless pinned". An MSP with a contractual
-- retention obligation of thirty days, or of seven, had to rebuild the binary
-- to keep it — so the number the screen displays is now the number it enforces,
-- and both are the same number.
--
-- The bounds are deliberately wide but not unbounded. Zero would mean a capture
-- is deleted as it arrives, and nothing is served by letting somebody configure
-- that by accident.
CREATE TABLE IF NOT EXISTS retention (
    id            BOOLEAN     PRIMARY KEY DEFAULT true CHECK (id),

    -- Diagnostic captures. These hold a customer's extension numbers, MAC
    -- addresses and internal addressing, which is why they expire at all.
    capture_days  INT         NOT NULL DEFAULT 14 CHECK (capture_days BETWEEN 1 AND 365),

    -- Cached tool results. Shorter by nature: this is a freshness budget, not
    -- a retention promise.
    cache_days    INT         NOT NULL DEFAULT 7  CHECK (cache_days BETWEEN 1 AND 90),

    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO retention (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
