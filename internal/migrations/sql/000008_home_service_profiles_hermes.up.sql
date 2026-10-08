ALTER TABLE home_service_profiles DROP CONSTRAINT IF EXISTS home_service_profiles_service_type_check;
ALTER TABLE home_service_profiles ADD CONSTRAINT home_service_profiles_service_type_check CHECK (service_type IN ('homeassistant', 'smb', 'hermes'));
