CREATE TABLE site_health (
                             site_id INTEGER PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
                             score INTEGER NOT NULL,
                             tier TEXT NOT NULL,
                             rendered BOOLEAN NOT NULL,
                             findings JSONB NOT NULL DEFAULT '[]'::jsonb,
                             checked_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_site_health_score ON site_health(score);
