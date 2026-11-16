-- Back to 000007's column and limit. The tags themselves are not restored: every clan gets an
-- empty one, as a clan made without a tag always had.
ALTER TABLE clans ADD COLUMN tag text NOT NULL DEFAULT '';
ALTER TABLE clans ADD CONSTRAINT clans_tag_length CHECK (char_length(tag) <= 8);
