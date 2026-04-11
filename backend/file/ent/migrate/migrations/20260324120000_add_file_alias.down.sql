DROP INDEX IF EXISTS file.file_tenant_id_public_alias;

ALTER TABLE file.files DROP COLUMN IF EXISTS public_alias;
