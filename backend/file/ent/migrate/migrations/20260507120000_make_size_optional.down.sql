UPDATE file.files SET size = 0 WHERE size IS NULL;

ALTER TABLE file.files ALTER COLUMN size SET NOT NULL;
