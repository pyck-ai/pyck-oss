-- create index "entityeventsoutbox_created_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_created_at" ON "event_outbox" ("created_at") WHERE ((dead_at IS NOT NULL) AND (published_at IS NULL));
