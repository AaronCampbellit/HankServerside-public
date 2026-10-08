DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM assistant_staged_attachments) THEN
  RAISE EXCEPTION 'Assistant attachment stages require backup and offline reconciliation before rollback';
 END IF;
END $$;
DROP TABLE assistant_staged_attachments;
