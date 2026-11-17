DROP TABLE IF EXISTS clan_join_requests;
ALTER TABLE clans DROP CONSTRAINT IF EXISTS clans_join_policy_check;
ALTER TABLE clans DROP COLUMN IF EXISTS join_policy;
