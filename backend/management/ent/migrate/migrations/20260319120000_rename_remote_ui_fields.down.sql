-- Rename remoteWebUI -> remoteReactUI and update /web/ -> /react/ in URL
UPDATE management.tenants
SET data = (data - 'remoteWebUI') || jsonb_build_object(
    'remoteReactUI',
    REPLACE(data->>'remoteWebUI', '/web/', '/react/')
)
WHERE data ? 'remoteWebUI';

-- Rename remoteMobileUI -> remoteFlutterUI and update /mobile/ -> /flutter/ in URL
UPDATE management.tenants
SET data = (data - 'remoteMobileUI') || jsonb_build_object(
    'remoteFlutterUI',
    REPLACE(data->>'remoteMobileUI', '/mobile/', '/flutter/')
)
WHERE data ? 'remoteMobileUI';
