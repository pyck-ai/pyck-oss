ALTER TABLE file.files ADD COLUMN IF NOT EXISTS public_alias VARCHAR NULL;

CREATE UNIQUE INDEX IF NOT EXISTS file_tenant_id_public_alias
    ON file.files (tenant_id, public_alias)
    WHERE deleted_at IS NULL AND public_alias IS NOT NULL;
