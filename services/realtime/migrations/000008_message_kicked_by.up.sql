-- Who removed the player, on a member_kicked line (TEAM-291 follow-up). Social's clan.member_left
-- event names them (kicked_by); the widget writes "Ali was removed by Emma". The name is the one the
-- kicker had at the time, like sender_name. Both are NULL together, and only a member_kicked message
-- has them (an event without kicked_by stores the line without them).
ALTER TABLE messages
    ADD COLUMN kicked_by_id   uuid,
    ADD COLUMN kicked_by_name text,
    ADD CONSTRAINT messages_kicked_by_check CHECK (
        (kicked_by_id IS NULL) = (kicked_by_name IS NULL)
        AND (kicked_by_id IS NULL OR kind = 'member_kicked'));
