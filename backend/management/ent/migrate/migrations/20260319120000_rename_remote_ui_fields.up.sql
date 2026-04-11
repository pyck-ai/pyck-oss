-- Rename remoteReactUI -> remoteWebUI and update /react/ -> /web/ in URL
UPDATE management.tenants
SET data = (data - 'remoteReactUI') || jsonb_build_object(
    'remoteWebUI',
    REPLACE(data->>'remoteReactUI', '/react/', '/web/')
)
WHERE data ? 'remoteReactUI';

-- Rename remoteFlutterUI -> remoteMobileUI and update /flutter/ -> /mobile/ in URL
UPDATE management.tenants
SET data = (data - 'remoteFlutterUI') || jsonb_build_object(
    'remoteMobileUI',
    REPLACE(data->>'remoteFlutterUI', '/flutter/', '/mobile/')
)
WHERE data ? 'remoteFlutterUI';
