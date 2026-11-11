-- Clan roles come from each game's config (clan.roles, config v1): a member's role is now a
-- role id such as "elder" or "co-leader", not only "owner" or "member". The rules (who may
-- promote or kick whom) live in the social service; the table only keeps the id.
--
-- "owner" and "member" stay valid: the owner row keeps "owner" (the owner is clans.owner_id,
-- always the last role), and "member" reads as the first role, as the SDK already does.
ALTER TABLE clan_members DROP CONSTRAINT clan_members_role_check;

-- Same form as a role id in config v1: lowercase letters, digits and single dashes, starts
-- with a letter, at most 24 characters.
ALTER TABLE clan_members ADD CONSTRAINT clan_members_role_check
    CHECK (role ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$' AND length(role) <= 24);
