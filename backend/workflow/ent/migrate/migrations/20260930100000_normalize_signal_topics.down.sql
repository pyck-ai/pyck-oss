-- Intentional no-op: re-adding the 'request.reply.' prefix cannot
-- distinguish topics normalized by the up migration from registrations
-- created in the fire-and-forget form to begin with, and would corrupt the
-- latter. The previous release matches fire-and-forget topics as well (its
-- SignalRouter ran a fire-and-forget subscription in parallel with the
-- request/reply one), so normalized registrations keep triggering after a
-- rollback.
SELECT 1;
