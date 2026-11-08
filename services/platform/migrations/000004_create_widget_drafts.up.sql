-- The widget config a developer is still editing in the Studio.
-- One draft per game; saving overwrites it. Players never see drafts,
-- only published versions (widget_versions).
create table widget_drafts (
                               game_id uuid primary key references games(id),
                               config jsonb not null,
                               updated_at timestamptz not null default now()
);