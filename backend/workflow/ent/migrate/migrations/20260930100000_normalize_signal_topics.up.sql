-- Normalize legacy request/reply signal topics to the fire-and-forget form.
--
-- Mutation events are consumed from the fire-and-forget subject
-- (<stream>.<tenant>.crud...), and signal topic matching is type-strict:
-- a signal stored with the 'request.reply.' prefix can never match an
-- incoming mutation event once events are no longer published via
-- request/reply. Strip the prefix ('request.reply.' = 14 characters) so
-- existing registrations keep triggering their workflows.
UPDATE workflow."workflow-signals"
SET nats_topic = substring(nats_topic FROM 15)
WHERE nats_topic LIKE 'request.reply.%';
