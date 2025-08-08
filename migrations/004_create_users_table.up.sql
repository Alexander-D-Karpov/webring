CREATE TABLE users (
                       id SERIAL PRIMARY KEY,
                       telegram_id BIGINT UNIQUE NOT NULL,
                       telegram_username TEXT,
                       first_name TEXT,
                       last_name TEXT,
                       is_admin BOOLEAN NOT NULL DEFAULT false,
                       created_at TIMESTAMP NOT NULL DEFAULT NOW()
);