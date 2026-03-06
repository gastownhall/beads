REPLACE INTO dolt_ignore VALUES ('wisps', true);
REPLACE INTO dolt_ignore VALUES ('wisp_%', true);
CALL DOLT_ADD('dolt_ignore');
CALL DOLT_COMMIT('-m', 'chore: add wisps patterns to dolt_ignore');
