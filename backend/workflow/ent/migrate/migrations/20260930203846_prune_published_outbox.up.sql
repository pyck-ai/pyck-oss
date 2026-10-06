-- create index "entityeventsoutbox_published_at" to table: "event_outbox"
CREATE INDEX "entityeventsoutbox_published_at" ON "event_outbox" ("published_at") WHERE (published_at IS NOT NULL);
