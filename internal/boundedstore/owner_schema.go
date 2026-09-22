package boundedstore

const ownerSchema = `
CREATE TABLE IF NOT EXISTS owner_drafts(
 id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0),
 target TEXT NOT NULL, target_revision INTEGER NOT NULL, kind TEXT NOT NULL,
 created TEXT NOT NULL, updated TEXT NOT NULL,
 payload TEXT NOT NULL REFERENCES payloads(id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS owner_draft_target ON owner_drafts(target,id);
CREATE INDEX IF NOT EXISTS owner_draft_payload ON owner_drafts(payload);
CREATE TABLE IF NOT EXISTS owner_draft_grants(
 id TEXT PRIMARY KEY, draft TEXT NOT NULL REFERENCES owner_drafts(id) ON DELETE CASCADE,
 principal TEXT NOT NULL, principal_revision INTEGER NOT NULL,
 owner_revision INTEGER NOT NULL, expires INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS owner_grant_draft ON owner_draft_grants(draft,id);
CREATE TABLE IF NOT EXISTS asset_originals(
 asset TEXT PRIMARY KEY, revision INTEGER NOT NULL,
 payload TEXT NOT NULL REFERENCES payloads(id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS original_payload ON asset_originals(payload);
CREATE TABLE IF NOT EXISTS owner_events(
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
 asset TEXT NOT NULL, revision INTEGER NOT NULL, operation TEXT NOT NULL,
 status TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS owner_event_age ON owner_events(at,sequence);
CREATE TABLE IF NOT EXISTS owner_event_relation_changes(
 sequence INTEGER PRIMARY KEY REFERENCES owner_events(sequence) ON DELETE CASCADE,
 quote_missing INTEGER NOT NULL CHECK(quote_missing>=0),
 quote_ambiguous INTEGER NOT NULL CHECK(quote_ambiguous>=0),
 target_unavailable INTEGER NOT NULL CHECK(target_unavailable>=0));
CREATE TABLE IF NOT EXISTS owner_operation_times(
 id TEXT PRIMARY KEY, state TEXT NOT NULL, updated INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS owner_operation_state ON owner_operation_times(state,updated,id);
INSERT OR IGNORE INTO store_meta VALUES('owner_event_floor',0),('owner_work_epoch',1);
`
