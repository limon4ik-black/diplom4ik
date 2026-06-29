-- migrations/001_init.sql

CREATE TABLE IF NOT EXISTS sessions (
    id          UInt64,
    started_at  DateTime64(3, 'UTC') NOT NULL,
    ended_at    Nullable(DateTime64(3, 'UTC')),
    status      LowCardinality(String) DEFAULT 'active'
    -- active | archived
)
ENGINE = ReplacingMergeTree()
ORDER BY id;

CREATE TABLE IF NOT EXISTS signal_logs (
    session_id  UInt64     NOT NULL,
    restart_id  UInt64     NOT NULL,
    element     LowCardinality(String) NOT NULL,
    parameter   LowCardinality(String) NOT NULL,
    model_time  Float64    NOT NULL,
    value       Float64    NOT NULL
)
ENGINE = MergeTree()
PARTITION BY session_id
ORDER BY (session_id, restart_id, model_time)
SETTINGS index_granularity = 8192;


CREATE TABLE IF NOT EXISTS event_logs (
    id          UUID       DEFAULT generateUUIDv4(),
    session_id  UInt64     NOT NULL,
    restart_id  UInt64     NOT NULL,
    username    LowCardinality(String) DEFAULT '',
    event_type  LowCardinality(String) NOT NULL,
    model_time  Float64    NOT NULL,
    data        String     DEFAULT '{}'
)
ENGINE = MergeTree()
PARTITION BY session_id
ORDER BY (session_id, restart_id, model_time)
SETTINGS index_granularity = 8192;
