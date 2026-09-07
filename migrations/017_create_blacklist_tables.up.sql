CREATE TABLE blacklist (
                           id SERIAL PRIMARY KEY,
                           subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'site')),
                           subject_value TEXT NOT NULL,
                           reason TEXT,
                           created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
                           created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                           UNIQUE (subject_type, subject_value)
);

CREATE INDEX idx_blacklist_subject_type ON blacklist(subject_type);

CREATE TABLE request_cooldowns (
                                   id SERIAL PRIMARY KEY,
                                   subject_type TEXT NOT NULL CHECK (subject_type IN ('user', 'site')),
                                   subject_value TEXT NOT NULL,
                                   reason TEXT,
                                   -- Both instants are compared against NOW() and written from Go, so they
                                   -- carry their offset: a TIMESTAMP here would be read back as a naive
                                   -- local time and skew every cooldown by the app's UTC offset.
                                   created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
                                   expires_at TIMESTAMPTZ NOT NULL,
                                   UNIQUE (subject_type, subject_value)
);

CREATE INDEX idx_request_cooldowns_expires_at ON request_cooldowns(expires_at);
