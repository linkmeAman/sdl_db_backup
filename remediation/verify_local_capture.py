"""Verify a historical per-database logical capture using configured storage."""
import argparse,json,gzip,hashlib,os,re
from pathlib import Path
from dotenv import dotenv_values
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--env',default=os.environ.get('BACKUP_ENV_FILE','.env'));p.add_argument('--run-id',required=True);p.add_argument('--output',default='/tmp/sdl-local-capture-check.json');a=p.parse_args()
if '/' in a.run_id or '..' in a.run_id:raise ValueError('invalid run id')
v=dict(dotenv_values(a.env));v.update({key:value for key,value in os.environ.items() if value.strip()})
root=Path(v.get('BACKUP_DIR') or Path.cwd()/'backups');folder=root/a.run_id;r=json.loads((folder/'manifest.json').read_text());out={'run_id':r['run_id'],'capture_status':r['status'],'databases':[],'cross_database_consistency':'NOT_GUARANTEED_BY_SEPARATE_DUMPS'}
for db in r['results']:
 p=folder/Path(db['output_path']).name;sqlsha=hashlib.sha256();tables=set();views=set();tail=b''
 with gzip.open(p,'rb') as f:
  for line in f:
   sqlsha.update(line);tail=(tail+line)[-512:]
   if line.startswith(b'CREATE TABLE '):
    m=re.search(rb'CREATE TABLE `([^`]+)`',line)
    if m:tables.add(m[1].decode())
   if b'VIEW `' in line and line.startswith(b'/*!'):
    m=re.search(rb'VIEW `([^`]+)`',line)
    if m:views.add(m[1].decode())
 item={'database':db['name'],'sql_sha256_match':sqlsha.hexdigest()==db.get('sql_sha256'),'dump_completed_footer':b'-- Dump completed' in tail,'base_table_count':len(tables-views),'view_count':len(views),'expected_base_table_count':db.get('table_count'),'expected_view_count':db.get('view_count',0)}
 item['object_counts_match']=item['base_table_count']==item['expected_base_table_count'] and item['view_count']==item['expected_view_count'];out['databases'].append(item);print(db['name'],item,flush=True)
Path(a.output).write_text(json.dumps(out,indent=2));print('local content verification complete',flush=True)
