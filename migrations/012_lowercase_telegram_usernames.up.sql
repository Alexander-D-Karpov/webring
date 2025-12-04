DO $$
    DECLARE
        dup_record RECORD;
        keep_id INTEGER;
        merge_ids INTEGER[];
    BEGIN
        FOR dup_record IN
            SELECT LOWER(telegram_username) as lower_username, array_agg(id ORDER BY
                CASE
                    WHEN telegram_id IS NOT NULL THEN 0
                    ELSE 1
                    END,
                created_at ASC
                                                               ) as user_ids
            FROM users
            WHERE telegram_username IS NOT NULL
            GROUP BY LOWER(telegram_username)
            HAVING COUNT(*) > 1
            LOOP
                keep_id := dup_record.user_ids[1];
                merge_ids := dup_record.user_ids[2:array_length(dup_record.user_ids, 1)];

                UPDATE users SET
                                 telegram_id = COALESCE(
                                         users.telegram_id,
                                         (SELECT telegram_id FROM users WHERE id = ANY(merge_ids) AND telegram_id IS NOT NULL LIMIT 1)
                                               ),
                                 first_name = COALESCE(
                                         users.first_name,
                                         (SELECT first_name FROM users WHERE id = ANY(merge_ids) AND first_name IS NOT NULL LIMIT 1)
                                              ),
                                 last_name = COALESCE(
                                         users.last_name,
                                         (SELECT last_name FROM users WHERE id = ANY(merge_ids) AND last_name IS NOT NULL LIMIT 1)
                                             ),
                                 is_admin = users.is_admin OR EXISTS(
                                     SELECT 1 FROM users WHERE id = ANY(merge_ids) AND is_admin = true
                                 )
                WHERE id = keep_id;

                UPDATE sites SET user_id = keep_id WHERE user_id = ANY(merge_ids);
                UPDATE update_requests SET user_id = keep_id WHERE user_id = ANY(merge_ids);
                UPDATE sessions SET user_id = keep_id WHERE user_id = ANY(merge_ids);

                DELETE FROM users WHERE id = ANY(merge_ids);

                RAISE NOTICE 'Merged users with username %: kept ID %, merged IDs %',
                    dup_record.lower_username, keep_id, merge_ids;
            END LOOP;
    END $$;

UPDATE users SET telegram_username = LOWER(telegram_username) WHERE telegram_username IS NOT NULL;

DROP INDEX IF EXISTS users_telegram_username_unique;

CREATE UNIQUE INDEX users_telegram_username_unique_lower ON users(LOWER(telegram_username))
    WHERE telegram_username IS NOT NULL;