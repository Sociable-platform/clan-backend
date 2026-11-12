-- Back to only "owner" and "member": every other role becomes "member".
ALTER TABLE clan_members DROP CONSTRAINT clan_members_role_check;
UPDATE clan_members SET role = 'member' WHERE role NOT IN ('owner', 'member');
ALTER TABLE clan_members ADD CONSTRAINT clan_members_role_check CHECK (role IN ('owner', 'member'));
