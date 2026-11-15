-- TEAM-276 step 2: clans have no tags, and config v1 no longer has clan.tags. A config that
-- still carries it breaks the schema (clan has additionalProperties: false), so it is removed
-- from every stored draft and published version first:
--   - a draft with it could no longer be saved again or published (publish validates the draft);
--   - an old version with it would come back on a rollback, which copies a version as it is.
--
-- Published versions are otherwise never changed. This is the one exception: the field did
-- nothing (no widget, SDK or service reads it), so every version looks and works the same after
-- it, and its version number and ETag stay as they are.
UPDATE widget_drafts   SET config = config #- '{clan,tags}' WHERE config -> 'clan' ? 'tags';
UPDATE widget_versions SET config = config #- '{clan,tags}' WHERE config -> 'clan' ? 'tags';
