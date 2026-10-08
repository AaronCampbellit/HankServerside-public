-- NULL means access remains valid until revoked. Existing grants retain their expiry.
ALTER TABLE fleet_grants ALTER COLUMN expires_at DROP NOT NULL;
