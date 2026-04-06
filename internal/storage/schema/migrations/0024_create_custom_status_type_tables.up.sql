CREATE TABLE IF NOT EXISTS custom_statuses (
    name VARCHAR(64) PRIMARY KEY,
    category VARCHAR(32) NOT NULL DEFAULT 'unspecified'
);

CREATE TABLE IF NOT EXISTS custom_types (
    name VARCHAR(64) PRIMARY KEY
);
