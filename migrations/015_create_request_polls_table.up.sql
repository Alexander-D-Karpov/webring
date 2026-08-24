CREATE TABLE request_polls (
                               poll_id TEXT PRIMARY KEY,
                               request_id INTEGER REFERENCES update_requests(id) ON DELETE SET NULL,
                               chat_id BIGINT NOT NULL,
                               message_id BIGINT NOT NULL,
                               threshold INTEGER NOT NULL,
                               status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'approved', 'declined', 'canceled')),
                               created_at TIMESTAMP NOT NULL DEFAULT NOW(),
                               closed_at TIMESTAMP
);

CREATE INDEX idx_request_polls_request_id ON request_polls(request_id);
CREATE INDEX idx_request_polls_status ON request_polls(status);

CREATE TABLE request_poll_votes (
                                    poll_id TEXT NOT NULL REFERENCES request_polls(poll_id) ON DELETE CASCADE,
                                    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
                                    telegram_id BIGINT NOT NULL,
                                    option_id INTEGER NOT NULL,
                                    voted_at TIMESTAMP NOT NULL DEFAULT NOW(),
                                    PRIMARY KEY (poll_id, telegram_id)
);

CREATE INDEX idx_request_poll_votes_poll_id ON request_poll_votes(poll_id);
