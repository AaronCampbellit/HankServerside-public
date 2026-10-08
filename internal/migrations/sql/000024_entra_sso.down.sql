DROP TABLE IF EXISTS external_identities;
ALTER TABLE users DROP COLUMN IF EXISTS password_login_enabled;
