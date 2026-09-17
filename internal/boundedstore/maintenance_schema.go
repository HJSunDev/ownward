package boundedstore

const maintenanceSchema = `
INSERT OR IGNORE INTO store_meta VALUES('run_epoch',0),('reclaim_bytes',0);
CREATE TABLE IF NOT EXISTS staging_owners(kind TEXT NOT NULL,id TEXT NOT NULL,epoch INTEGER NOT NULL,PRIMARY KEY(kind,id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS staging_epoch ON staging_owners(epoch,kind,id);
CREATE TABLE IF NOT EXISTS reclaim_estimates(kind TEXT NOT NULL,id TEXT NOT NULL,bytes INTEGER NOT NULL,PRIMARY KEY(kind,id)) WITHOUT ROWID;
CREATE TRIGGER IF NOT EXISTS reclaim_estimate_insert AFTER INSERT ON reclaim_estimates BEGIN UPDATE store_meta SET value=value+NEW.bytes WHERE key='reclaim_bytes'; END;
CREATE TRIGGER IF NOT EXISTS reclaim_estimate_delete AFTER DELETE ON reclaim_estimates BEGIN UPDATE store_meta SET value=value-OLD.bytes WHERE key='reclaim_bytes'; END;
CREATE TRIGGER IF NOT EXISTS reclaim_estimate_update AFTER UPDATE ON reclaim_estimates BEGIN UPDATE store_meta SET value=value+NEW.bytes-OLD.bytes WHERE key='reclaim_bytes'; END;
CREATE TRIGGER IF NOT EXISTS payload_staging_size AFTER INSERT ON content_chunks BEGIN
 UPDATE payloads SET content_bytes=content_bytes+CASE WHEN NEW.part=0 THEN length(NEW.bytes) ELSE 0 END,details_bytes=details_bytes+CASE WHEN NEW.part=1 THEN length(NEW.bytes) ELSE 0 END WHERE id=NEW.payload AND state='staging';
END;
CREATE TRIGGER IF NOT EXISTS payload_reclaim_size AFTER INSERT ON reclaim_jobs BEGIN
 INSERT OR IGNORE INTO reclaim_estimates SELECT 'payload',NEW.payload,content_bytes+details_bytes FROM payloads WHERE id=NEW.payload;
END;
CREATE TRIGGER IF NOT EXISTS payload_reclaim_done AFTER DELETE ON reclaim_jobs BEGIN DELETE FROM reclaim_estimates WHERE kind='payload' AND id=OLD.payload; END;
CREATE TRIGGER IF NOT EXISTS payload_reclaim_progress AFTER DELETE ON content_chunks BEGIN UPDATE reclaim_estimates SET bytes=max(0,bytes-length(OLD.bytes)) WHERE kind='payload' AND id=OLD.payload; END;
CREATE TRIGGER IF NOT EXISTS derived_reclaim_size AFTER INSERT ON derived_reclaim WHEN NEW.kind='organization' BEGIN
 INSERT OR IGNORE INTO reclaim_estimates SELECT NEW.kind,NEW.id,coalesce(sum(length(bytes)),0) FROM organization_chunks WHERE organization=NEW.id;
END;
CREATE TRIGGER IF NOT EXISTS derived_reclaim_done AFTER DELETE ON derived_reclaim BEGIN DELETE FROM reclaim_estimates WHERE kind=OLD.kind AND id=OLD.id; END;
CREATE TRIGGER IF NOT EXISTS derived_reclaim_progress AFTER DELETE ON organization_chunks BEGIN UPDATE reclaim_estimates SET bytes=max(0,bytes-length(OLD.bytes)) WHERE kind='organization' AND id=OLD.organization; END;
CREATE TRIGGER IF NOT EXISTS payload_stage_owner AFTER INSERT ON payloads BEGIN INSERT INTO staging_owners SELECT 'payload',NEW.id,value FROM store_meta WHERE key='run_epoch'; END;
CREATE TRIGGER IF NOT EXISTS payload_stage_published AFTER UPDATE OF state ON payloads WHEN NEW.state='published' BEGIN DELETE FROM staging_owners WHERE kind='payload' AND id=NEW.id; END;
CREATE TRIGGER IF NOT EXISTS organization_stage_owner AFTER INSERT ON organizations BEGIN INSERT INTO staging_owners SELECT 'organization',NEW.id,value FROM store_meta WHERE key='run_epoch'; END;
CREATE TRIGGER IF NOT EXISTS organization_stage_published AFTER UPDATE OF state ON organizations WHEN NEW.state='active' BEGIN DELETE FROM staging_owners WHERE kind='organization' AND id=NEW.id; END;
CREATE TRIGGER IF NOT EXISTS block_stage_owner AFTER INSERT ON vector_blocks BEGIN INSERT INTO staging_owners SELECT 'vector_block',NEW.id,value FROM store_meta WHERE key='run_epoch'; END;
CREATE TRIGGER IF NOT EXISTS block_stage_published AFTER UPDATE OF state ON vector_blocks WHEN NEW.state='active' BEGIN DELETE FROM staging_owners WHERE kind='vector_block' AND id=NEW.id; END;
CREATE TABLE IF NOT EXISTS invalidation_jobs(asset TEXT NOT NULL,revision INTEGER NOT NULL,snapshot TEXT NOT NULL,forget INTEGER NOT NULL,cursor TEXT NOT NULL DEFAULT '',PRIMARY KEY(asset,revision,snapshot,forget)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS forget_operations(id TEXT PRIMARY KEY,principal TEXT NOT NULL,digest TEXT NOT NULL,state TEXT NOT NULL,targets INTEGER NOT NULL DEFAULT 0,terms INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS forget_targets(operation TEXT NOT NULL REFERENCES forget_operations(id),asset TEXT NOT NULL,revision INTEGER NOT NULL,PRIMARY KEY(operation,asset)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS forget_target_asset ON forget_targets(asset,operation);
CREATE VIEW IF NOT EXISTS live_assets AS SELECT a.* FROM assets a WHERE a.deleted=0 AND NOT EXISTS(SELECT 1 FROM forget_targets t JOIN forget_operations f ON f.id=t.operation WHERE t.asset=a.id AND f.state IN ('cleaning','complete'));
CREATE INDEX IF NOT EXISTS control_payload ON control_records(payload);
CREATE INDEX IF NOT EXISTS organization_state ON organizations(state,id);
CREATE INDEX IF NOT EXISTS block_state ON vector_blocks(state,id);
CREATE INDEX IF NOT EXISTS cursor_expiry ON navigation_cursors(expires,id);
CREATE TABLE IF NOT EXISTS controlled_copies(id TEXT PRIMARY KEY,epoch INTEGER NOT NULL,state TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS organization_heads(generation TEXT NOT NULL,asset TEXT NOT NULL,organization TEXT NOT NULL,PRIMARY KEY(generation,asset)) WITHOUT ROWID;
`
