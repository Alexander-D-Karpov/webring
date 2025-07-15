ALTER TABLE users ALTER COLUMN telegram_id DROP NOT NULL;

UPDATE users SET telegram_id = NULL WHERE telegram_id = 0;