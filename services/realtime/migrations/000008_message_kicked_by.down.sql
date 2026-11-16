ALTER TABLE messages
    DROP CONSTRAINT IF EXISTS messages_kicked_by_check,
    DROP COLUMN IF EXISTS kicked_by_id,
    DROP COLUMN IF EXISTS kicked_by_name;
