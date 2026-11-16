-- The member lines go with the column: without it they would read as empty text messages.
DELETE FROM messages WHERE kind <> 'text';
ALTER TABLE messages DROP COLUMN IF EXISTS kind;
