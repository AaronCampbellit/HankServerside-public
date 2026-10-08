DO $migration$
DECLARE
	invalid_child RECORD;
BEGIN
	SELECT
		child.id,
		child.note_id,
		child.owner_user_id,
		child.parent_id
	INTO invalid_child
	FROM user_notes child
	LEFT JOIN user_notes parent
		ON parent.owner_user_id = child.owner_user_id
		AND parent.note_id = child.parent_id
	WHERE child.parent_id IS NOT NULL
		AND (
			parent.id IS NULL
			OR (
				child.deleted_at IS NULL
				AND (
					child.note_id = child.parent_id
					OR parent.deleted_at IS NOT NULL
					OR parent.page_type <> 'notebook'
				)
			)
		)
	ORDER BY child.id
	LIMIT 1;

	IF FOUND THEN
		RAISE EXCEPTION
			'cannot enforce note notebook integrity: note % (owner %, parent %) has an invalid parent',
			invalid_child.note_id,
			invalid_child.owner_user_id,
			invalid_child.parent_id
			USING ERRCODE = '23514';
	END IF;
END;
$migration$;

ALTER TABLE user_notes
	ADD CONSTRAINT user_notes_owner_parent_fkey
	FOREIGN KEY (owner_user_id, parent_id)
	REFERENCES user_notes(owner_user_id, note_id)
	ON UPDATE RESTRICT
	ON DELETE RESTRICT;

CREATE FUNCTION validate_user_note_parent()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
DECLARE
	parent_page_type TEXT;
	parent_deleted_at TIMESTAMP;
BEGIN
	IF NEW.deleted_at IS NOT NULL OR NEW.parent_id IS NULL THEN
		RETURN NEW;
	END IF;

	IF NEW.parent_id = NEW.note_id THEN
		RAISE EXCEPTION
			'note % cannot be its own notebook parent',
			NEW.note_id
			USING ERRCODE = '23514';
	END IF;

	SELECT page_type, deleted_at
	INTO parent_page_type, parent_deleted_at
	FROM user_notes
	WHERE owner_user_id = NEW.owner_user_id
		AND note_id = NEW.parent_id
	FOR UPDATE;

	IF NOT FOUND THEN
		RAISE EXCEPTION
			'parent_id % must refer to a notebook owned by user %',
			NEW.parent_id,
			NEW.owner_user_id
			USING ERRCODE = '23514';
	END IF;

	IF parent_deleted_at IS NOT NULL OR parent_page_type <> 'notebook' THEN
		RAISE EXCEPTION
			'parent_id % must refer to an active notebook owned by user %',
			NEW.parent_id,
			NEW.owner_user_id
			USING ERRCODE = '23514';
	END IF;

	RETURN NEW;
END;
$function$;

CREATE TRIGGER user_notes_validate_parent
BEFORE INSERT OR UPDATE OF owner_user_id, note_id, parent_id, deleted_at
ON user_notes
FOR EACH ROW
EXECUTE FUNCTION validate_user_note_parent();

CREATE FUNCTION protect_user_note_notebook_parent()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
	IF OLD.deleted_at IS NULL
		AND OLD.page_type = 'notebook'
		AND (NEW.deleted_at IS NOT NULL OR NEW.page_type <> 'notebook')
		AND EXISTS (
			SELECT 1
			FROM user_notes child
			WHERE child.owner_user_id = OLD.owner_user_id
				AND child.parent_id = OLD.note_id
				AND child.deleted_at IS NULL
		)
	THEN
		RAISE EXCEPTION
			'notebook % has active child notes; move them before deleting or changing its type',
			OLD.note_id
			USING ERRCODE = '23514';
	END IF;

	RETURN NEW;
END;
$function$;

CREATE TRIGGER user_notes_protect_notebook_parent
BEFORE UPDATE OF page_type, deleted_at
ON user_notes
FOR EACH ROW
EXECUTE FUNCTION protect_user_note_notebook_parent();
