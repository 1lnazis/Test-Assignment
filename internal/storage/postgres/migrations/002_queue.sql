ALTER TABLE quote_updates
    ADD COLUMN attempts integer NOT NULL DEFAULT 0
        CONSTRAINT quote_updates_attempts_nonnegative CHECK (attempts >= 0),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN locked_until timestamptz;

UPDATE quote_updates
SET locked_until = now()
WHERE status = 'processing';

ALTER TABLE quote_updates
    ADD CONSTRAINT quote_updates_processing_has_lease
    CHECK ((status = 'processing') = (locked_until IS NOT NULL));

CREATE INDEX quote_updates_queue_idx
    ON quote_updates (
        (CASE WHEN status = 'pending' THEN next_attempt_at ELSE locked_until END),
        created_at,
        id
    )
    WHERE status IN ('pending', 'processing');
