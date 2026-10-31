-- Запросы на обновление котировок и их результаты.
CREATE TABLE quote_updates (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    base       text        NOT NULL CHECK (base  ~ '^[A-Z]{3}$'),
    quote      text        NOT NULL CHECK (quote ~ '^[A-Z]{3}$'),
    status     text        NOT NULL CHECK (status IN ('pending', 'processing', 'done', 'failed')),
    price      numeric(24, 10),
    provider   text,
    error      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT different_currencies CHECK (base <> quote),
    -- цена есть ровно у выполненных запросов
    CONSTRAINT price_only_when_done CHECK ((status = 'done') = (price IS NOT NULL))
);

-- Последняя котировка по паре: быстрый поиск среди выполненных запросов.
CREATE INDEX quote_updates_latest_idx
    ON quote_updates (base, quote, updated_at DESC)
    WHERE status = 'done';
