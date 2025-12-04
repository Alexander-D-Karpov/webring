DO $$
    DECLARE
        keep_id INTEGER;
        merge_ids INTEGER[];
        anon_users INTEGER[];
    BEGIN
        SELECT array_agg(id ORDER BY created_at ASC) INTO anon_users
        FROM users
        WHERE telegram_id IS NULL AND telegram_username IS NULL;

        IF array_length(anon_users, 1) > 1 THEN
            keep_id := anon_users[1];
            merge_ids := anon_users[2:array_length(anon_users, 1)];

            UPDATE users SET
                is_admin = users.is_admin OR EXISTS(
                    SELECT 1 FROM users WHERE id = ANY(merge_ids) AND is_admin = true
                )
            WHERE id = keep_id;

            UPDATE sites SET user_id = keep_id WHERE user_id = ANY(merge_ids);
            UPDATE update_requests SET user_id = keep_id WHERE user_id = ANY(merge_ids);
            UPDATE sessions SET user_id = keep_id WHERE user_id = ANY(merge_ids);

            DELETE FROM users WHERE id = ANY(merge_ids);

            RAISE NOTICE 'Merged anonymous users: kept ID %, merged IDs %', keep_id, merge_ids;
        END IF;
    END $$;

DROP INDEX IF EXISTS users_telegram_username_unique_lower;
DROP INDEX IF EXISTS users_telegram_username_unique;

CREATE UNIQUE INDEX users_telegram_username_unique_lower ON users(LOWER(telegram_username))
    WHERE telegram_username IS NOT NULL;

CREATE UNIQUE INDEX users_single_anonymous ON users((1))
    WHERE telegram_id IS NULL AND telegram_username IS NULL;