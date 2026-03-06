CREATE TABLE IF NOT EXISTS federation_peers (
    name VARCHAR(255) PRIMARY KEY,
    remote_url VARCHAR(1024) NOT NULL,
    username VARCHAR(255),
    password_encrypted BLOB,
    sovereignty VARCHAR(8) DEFAULT '',
    last_sync DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_federation_peers_sovereignty (sovereignty)
);
