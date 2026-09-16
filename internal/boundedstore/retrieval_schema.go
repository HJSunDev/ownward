package boundedstore

const retrievalSchema = `
CREATE TABLE IF NOT EXISTS lexical_documents(payload TEXT PRIMARY KEY REFERENCES payloads(id), asset TEXT NOT NULL, length INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS postings(term BLOB NOT NULL, payload TEXT NOT NULL, frequency INTEGER NOT NULL, PRIMARY KEY(term,payload)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS postings_payload ON postings(payload,term);
CREATE TABLE IF NOT EXISTS lexical_contexts(payload TEXT NOT NULL, ordinal INTEGER NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, lower_key BLOB NOT NULL,lower_value BLOB NOT NULL, PRIMARY KEY(payload,ordinal)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS lexical_stats(singleton INTEGER PRIMARY KEY CHECK(singleton=1), documents INTEGER NOT NULL, terms INTEGER NOT NULL);
INSERT OR IGNORE INTO lexical_stats VALUES(1,0,0);
CREATE INDEX IF NOT EXISTS assets_payload ON assets(payload);
CREATE TABLE IF NOT EXISTS generations(id TEXT PRIMARY KEY, space TEXT NOT NULL, state TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS derived_state(singleton INTEGER PRIMARY KEY CHECK(singleton=1), generation TEXT NOT NULL, epoch INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS organizations(id TEXT PRIMARY KEY,generation TEXT NOT NULL REFERENCES generations(id),asset TEXT NOT NULL,revision INTEGER NOT NULL,snapshot TEXT NOT NULL,state TEXT NOT NULL,status TEXT NOT NULL,work_id TEXT NOT NULL,expected TEXT NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS organizations_owner ON organizations(generation,asset,state);
CREATE TABLE IF NOT EXISTS organization_current(generation TEXT NOT NULL,asset TEXT NOT NULL,organization TEXT NOT NULL REFERENCES organizations(id),PRIMARY KEY(generation,asset)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS organization_current_owner ON organization_current(organization,generation,asset);
CREATE TABLE IF NOT EXISTS organization_publications(sequence INTEGER PRIMARY KEY AUTOINCREMENT,organization TEXT NOT NULL UNIQUE REFERENCES organizations(id));
CREATE TABLE IF NOT EXISTS organization_headers(organization TEXT PRIMARY KEY,data BLOB NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS organization_chunks(organization TEXT NOT NULL REFERENCES organizations(id),ordinal INTEGER NOT NULL,bytes BLOB NOT NULL CHECK(length(bytes)<=65536),PRIMARY KEY(organization,ordinal)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS dependencies(organization TEXT NOT NULL REFERENCES organizations(id),asset TEXT NOT NULL,revision INTEGER NOT NULL,snapshot TEXT NOT NULL,PRIMARY KEY(organization,asset,revision,snapshot)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS dependencies_input ON dependencies(asset,organization);
CREATE TABLE IF NOT EXISTS organization_contexts(organization TEXT NOT NULL,ordinal INTEGER NOT NULL,key BLOB NOT NULL,value BLOB NOT NULL,lower_key BLOB NOT NULL,PRIMARY KEY(organization,ordinal)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS vectors(organization TEXT PRIMARY KEY REFERENCES organizations(id),space TEXT NOT NULL,dimensions INTEGER NOT NULL,data BLOB NOT NULL,digest BLOB NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS vector_delta(organization TEXT PRIMARY KEY REFERENCES vectors(organization),space TEXT NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS vector_delta_space ON vector_delta(space,organization);
CREATE TABLE IF NOT EXISTS vector_blocks(id TEXT PRIMARY KEY,space TEXT NOT NULL,dimensions INTEGER NOT NULL,state TEXT NOT NULL,header BLOB NOT NULL,digest BLOB NOT NULL,members INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS vector_blocks_space ON vector_blocks(space,state,id);
CREATE TABLE IF NOT EXISTS vector_members(block TEXT NOT NULL REFERENCES vector_blocks(id),ordinal INTEGER NOT NULL,organization TEXT NOT NULL,PRIMARY KEY(block,ordinal)) WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS vector_member_owner ON vector_members(organization,block);
CREATE TABLE IF NOT EXISTS vector_filter_pages(block TEXT NOT NULL REFERENCES vector_blocks(id),level INTEGER NOT NULL,page INTEGER NOT NULL,data BLOB NOT NULL,digest BLOB NOT NULL,PRIMARY KEY(block,level,page)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS derived_reclaim(id TEXT PRIMARY KEY,kind TEXT NOT NULL,reason TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS graph_links(organization TEXT NOT NULL,ordinal INTEGER NOT NULL,source TEXT NOT NULL,target TEXT NOT NULL,type TEXT NOT NULL,grounded INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(organization,ordinal)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS graph_forward ON graph_links(source,grounded,organization,ordinal);
CREATE INDEX IF NOT EXISTS graph_reverse ON graph_links(target,grounded,organization,ordinal);
CREATE TABLE IF NOT EXISTS graph_explicit(organization TEXT NOT NULL,target TEXT NOT NULL,PRIMARY KEY(organization,target)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS graph_units(organization TEXT NOT NULL,id TEXT NOT NULL,fingerprint TEXT NOT NULL,ordinal INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(organization,id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS graph_unit_fingerprint ON graph_units(organization,fingerprint,id);
CREATE TABLE IF NOT EXISTS graph_names(term TEXT NOT NULL,organization TEXT NOT NULL,PRIMARY KEY(term,organization)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS navigation_cursors(id TEXT PRIMARY KEY,principal TEXT NOT NULL,generation TEXT NOT NULL,asset_epoch INTEGER NOT NULL,derived_epoch INTEGER NOT NULL,expires INTEGER NOT NULL,state BLOB NOT NULL CHECK(length(state)<=65536)) WITHOUT ROWID;
`
