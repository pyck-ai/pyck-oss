-- Re-point undelivered legacy outbox rows at the fire-and-forget subject.
--
-- Rows written before the reply-wait removal carry the legacy
-- 'request.reply.' subject prefix in "topic". The outbox drainer now
-- publishes every row via JetStream, and no stream captures 'request.>',
-- so such rows could never be delivered: they would exhaust their retries
-- and dead-letter, silently losing the workflow trigger they carry.
-- Stripping the prefix re-points them at the subject the workflow service
-- subscribes to. The retry state is reset because previous failures belong
-- to the removed request/reply delivery mode. Rows already published or
-- dead are history and stay untouched.
UPDATE "event_outbox"
SET "topic"         = substring("topic" FROM 15),
    "retry_count"   = 0,
    "next_retry_at" = NULL,
    "last_error"    = NULL
WHERE "topic" LIKE 'request.reply.%'
  AND "published_at" IS NULL
  AND "dead_at" IS NULL;
