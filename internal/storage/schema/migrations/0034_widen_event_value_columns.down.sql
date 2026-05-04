ALTER TABLE wisp_events MODIFY new_value TEXT DEFAULT '';
ALTER TABLE wisp_events MODIFY old_value TEXT DEFAULT '';
ALTER TABLE events MODIFY new_value TEXT;
ALTER TABLE events MODIFY old_value TEXT;
