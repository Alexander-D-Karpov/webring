DROP INDEX IF EXISTS users_telegram_username_unique_lower;

CREATE UNIQUE INDEX users_telegram_username_unique ON users(telegram_username)
    WHERE telegram_username IS NOT NULL;