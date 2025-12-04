DROP INDEX IF EXISTS users_single_anonymous;
DROP INDEX IF EXISTS users_telegram_username_unique_lower;

CREATE UNIQUE INDEX users_telegram_username_unique_lower ON users(LOWER(telegram_username))
    WHERE telegram_username IS NOT NULL;