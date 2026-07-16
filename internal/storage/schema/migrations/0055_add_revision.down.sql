-- Down migrations are conventional only (not embedded/applied). Kept for parity.
ALTER TABLE issues DROP COLUMN revision;
ALTER TABLE wisps DROP COLUMN revision;
