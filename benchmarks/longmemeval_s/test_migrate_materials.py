import json
from pathlib import Path
import sqlite3
import struct
import tempfile
import unittest
import zlib

from migrate_materials import verify_compatible


class PreparedStorageMigrationTest(unittest.TestCase):
    def test_exact_content_semantics_vectors_and_completion_are_required(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); old = root/'old'; new = root/'new'
            (old/'assets').mkdir(parents=True); (old/'state').mkdir()
            (new/'stores/id').mkdir(parents=True)
            original = {'id':'a','revision':1,'content':'保留原文\n','source':{'ref':'original'}}
            (old/'assets/information.jsonl').write_text(json.dumps({'operation':'create','value':original})+'\n', encoding='utf-8')
            semantic = {'asset_id':'a','asset_revision':1,'status':'ready','analysis':{'summary':'保留原文'}}
            raw = json.dumps(semantic).encode(); vector = struct.pack('<ff',1.0,0.25)
            (old/'state/organization.binlog').write_bytes(b'OWD3'+struct.pack('<III',len(raw),len(vector),zlib.crc32(raw+vector))+raw+vector+b'DONE')
            (new/'storage.json').write_text(json.dumps({'id':'id'}),encoding='utf-8')
            c = sqlite3.connect(new/'stores/id/ownward.sqlite')
            c.executescript('''CREATE TABLE live_assets(id,revision,payload,deleted);
                CREATE TABLE content_chunks(payload,part,ordinal,bytes);
                CREATE TABLE organization_current(asset,organization);
                CREATE TABLE organization_chunks(organization,ordinal,bytes);
                CREATE TABLE vectors(organization,data);
                CREATE TABLE semantic_jobs(asset);''')
            c.execute("INSERT INTO live_assets VALUES('a',1,'p',0)")
            c.execute("INSERT INTO content_chunks VALUES('p',0,0,?)", (original['content'].encode(),))
            c.execute("INSERT INTO content_chunks VALUES('p',1,0,?)", (json.dumps({'source':original['source']}).encode(),))
            c.execute("INSERT INTO organization_current VALUES('a','o')")
            c.execute("INSERT INTO organization_chunks VALUES('o',0,?)", (raw,))
            c.execute("INSERT INTO vectors VALUES('o',?)", (vector,)); c.commit()
            self.assertTrue(verify_compatible(old,new)['vectors_bitwise_equal'])
            for statement, reset in [
                ("UPDATE content_chunks SET bytes=x'78' WHERE part=0", ("UPDATE content_chunks SET bytes=? WHERE part=0", (original['content'].encode(),))),
                ("UPDATE organization_chunks SET bytes=x'7b7d'", ("UPDATE organization_chunks SET bytes=?", (raw,))),
                ("UPDATE vectors SET data=x'0000'", ("UPDATE vectors SET data=?", (vector,))),
                ("INSERT INTO semantic_jobs VALUES('a')", ("DELETE FROM semantic_jobs", ())),
            ]:
                c.execute(statement); c.commit()
                with self.assertRaises(ValueError): verify_compatible(old,new)
                c.execute(*reset); c.commit()
            c.close()


if __name__ == '__main__':
    unittest.main()
