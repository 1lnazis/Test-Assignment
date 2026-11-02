-- Счётчик попыток и время следующей доступности сохраняются между перезапусками.
ALTER TABLE quote_updates
    ADD COLUMN attempts integer NOT NULL DEFAULT 0
        CONSTRAINT quote_updates_attempts_nonnegative CHECK (attempts >= 0),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN locked_until timestamptz;

-- У старых processing-записей ещё нет аренды: делаем её сразу истёкшей,
-- чтобы новый ClaimNext мог безопасно вернуть их в обработку.
UPDATE quote_updates
SET locked_until = now()
WHERE status = 'processing';

ALTER TABLE quote_updates
    ADD CONSTRAINT quote_updates_processing_has_lease
    CHECK ((status = 'processing') = (locked_until IS NOT NULL));

-- Для pending время готовности — next_attempt_at, для processing — конец аренды.
-- Индекс совпадает с фильтром и порядком ClaimNext. Текущее время в индекс
-- не входит: условие ready_at <= now() применяется самим запросом.
CREATE INDEX quote_updates_queue_idx
    ON quote_updates (
        (CASE WHEN status = 'pending' THEN next_attempt_at ELSE locked_until END),
        created_at,
        id
    )
    WHERE status IN ('pending', 'processing');
