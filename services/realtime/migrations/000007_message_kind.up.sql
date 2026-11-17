-- What a message is (TEAM-291). "text" is a message a player wrote; the others are lines Realtime
-- writes in a clan room when a player joins, leaves or is kicked: sender_id and sender_name are that
-- player, and body is empty (the widget writes the sentence). Existing messages are all "text".
-- Unread counts read only "text", so a join or leave gives nobody a badge.
ALTER TABLE messages
    ADD COLUMN kind text NOT NULL DEFAULT 'text'
        CONSTRAINT messages_kind_check CHECK (kind IN ('text', 'member_joined', 'member_left', 'member_kicked'));
