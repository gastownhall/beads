ALTER TABLE issues ADD COLUMN created_by_session VARCHAR(255) DEFAULT '';
ALTER TABLE wisps ADD COLUMN created_by_session VARCHAR(255) DEFAULT '';
ALTER TABLE issues ADD COLUMN claimed_by_session VARCHAR(255) DEFAULT '';
ALTER TABLE wisps ADD COLUMN claimed_by_session VARCHAR(255) DEFAULT '';
