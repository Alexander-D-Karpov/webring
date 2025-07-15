CREATE TABLE update_requests (
                                 id SERIAL PRIMARY KEY,
                                 user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                 site_id INTEGER REFERENCES sites(id) ON DELETE CASCADE,
                                 request_type TEXT NOT NULL CHECK (request_type IN ('create', 'update')),
                                 changed_fields JSONB NOT NULL,
                                 created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_update_requests_user_id ON update_requests(user_id);
CREATE INDEX idx_update_requests_site_id ON update_requests(site_id);