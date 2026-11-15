-- TEAM-277: clans have no tags. The widget draws none, the Create form has no Tag field, and the
-- widget config no longer has clan.tags (TEAM-276), so the column added by 000007 goes. Social
-- still accepts a "tag" in POST /api/v1/clans from older clients, and ignores it.
ALTER TABLE clans DROP CONSTRAINT IF EXISTS clans_tag_length;
ALTER TABLE clans DROP COLUMN IF EXISTS tag;
