CREATE TABLE sites (
                       id SERIAL PRIMARY KEY,
                       name TEXT NOT NULL,
                       url TEXT NOT NULL,
                       is_up BOOLEAN NOT NULL DEFAULT true,
                       last_check FLOAT NOT NULL DEFAULT 0
);