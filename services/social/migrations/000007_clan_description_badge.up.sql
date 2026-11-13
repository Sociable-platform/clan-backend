-- TEAM-184: a clan can say what it is about, wear a badge and carry a short tag.
--
-- All three are empty for the clans that already exist, so nothing has to be filled in and
-- a game whose clan form does not ask for them keeps working.
--
-- The badge is only an id such as 'wolf', never a picture: the game developer adds the
-- image to the game's widget config in Studio and ships the same asset with the game, so
-- social stores the id and never serves files.
ALTER TABLE clans
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN badge       text NOT NULL DEFAULT '',
    ADD COLUMN tag         text NOT NULL DEFAULT '';

-- The service checks the same limits and gives a clean 400; these are the last guard.
-- char_length counts characters, not bytes, so 300 characters fit in any language.
-- 300 is also the most a game can set as clan.descriptionMax in its widget config.
ALTER TABLE clans
    ADD CONSTRAINT clans_description_length CHECK (char_length(description) <= 300),
    ADD CONSTRAINT clans_badge_length       CHECK (char_length(badge) <= 64),
    ADD CONSTRAINT clans_tag_length         CHECK (char_length(tag) <= 8);
