-- Intentional no-op: re-adding the 'request.reply.' prefix cannot
-- distinguish rows normalized by the up migration from rows written in the
-- fire-and-forget form to begin with, and would corrupt the latter. The
-- previous release also delivers fire-and-forget subjects (its drainer
-- published a fire-and-forget copy and the workflow service subscribed to
-- it), so normalized rows keep working after a rollback.
SELECT 1;
