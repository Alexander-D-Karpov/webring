UPDATE users SET telegram_id = -(ROW_NUMBER() OVER (ORDER BY id)) WHERE telegram_id IS NULL;

ALTER TABLE users ALTER COLUMN telegram_id SET NOT NULL;