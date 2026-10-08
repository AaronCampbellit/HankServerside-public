-- Never silently discard configured delivery credentials on rollback.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM monitoring_delivery_settings) THEN
        RAISE EXCEPTION 'monitoring settings exist; preserve configuration before rollback';
    END IF;
END $$;
DROP TABLE monitoring_delivery_settings;
