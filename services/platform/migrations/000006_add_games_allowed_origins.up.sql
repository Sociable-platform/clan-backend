-- The websites that may open a WebSocket to realtime for this game, as the browser writes them in
-- the Origin header (scheme://host[:port]). Realtime reads it from platform; empty means the
-- developer has not set any.
alter table games add column allowed_origins text[] not null default '{}';
