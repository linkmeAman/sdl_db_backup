#!/usr/bin/env python3
"""Read-only production metadata/inventory audit. Never deletes or restores."""
import argparse, datetime, hashlib, json, os, re, sys
from pathlib import Path
import boto3, pymysql
from dotenv import dotenv_values
from botocore.config import Config
p=argparse.ArgumentParser();p.add_argument('--env',default='.env');p.add_argument('--output',required=True);p.add_argument('--verify-logical-payloads',action='store_true');a=p.parse_args()
v=dotenv_values(a.env); out={'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'limitations':[]}
def fail(e): return {'error_type':type(e).__name__,'code':getattr(e,'response',{}).get('Error',{}).get('Code') or (e.args[0] if e.args and isinstance(e.args[0],int) else None)}
root=Path(v['BACKUP_DIR']); st=os.statvfs(root)
out['storage']={'free_bytes':st.f_bavail*st.f_frsize,'total_bytes':st.f_blocks*st.f_frsize,'free_inodes':st.f_favail}
out['local']=[]
for d in sorted(root.iterdir()):
 if not d.is_dir() or not re.fullmatch(r'\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}',d.name):continue
 r={'run_id':d.name,'artifacts':[]}
 try:
  m=json.loads((d/'manifest.json').read_text());r['status']=m.get('status');r['expected_databases']=[x['name'] for x in m.get('results',[])];r['validation_status']=m.get('validation_status')
  for x in m.get('results',[]):
   if x.get('status')!='success':continue
   f=d/Path(x.get('output_path','')).name;h=hashlib.sha256()
   if not f.is_file():r['artifacts'].append({'name':f.name,'verification':'MISSING'});continue
   with f.open('rb') as stream:
    for b in iter(lambda:stream.read(1024*1024),b''):h.update(b)
   r['artifacts'].append({'name':f.name,'size':f.stat().st_size,'sha256_match':h.hexdigest()==x.get('artifact_sha256'),'recorded_sha256':bool(x.get('artifact_sha256'))})
 except Exception as e:r['error']=fail(e)
 out['local'].append(r)
print('local inventory complete',flush=True)
try:
 c=pymysql.connect(host=v['DB_HOST'],port=int(v['DB_PORT']),user=v['DB_USER'],password=v['DB_PASS'],connect_timeout=10,read_timeout=20,cursorclass=pymysql.cursors.DictCursor)
 with c.cursor() as cur:
  def q(sql,args=None):
   try:cur.execute(sql,args);return cur.fetchall()
   except Exception as e:return fail(e)
  db='pf_TickleRight_9210';view='co_eff_monthly_view_new'
  out['mysql']={'server':q('SELECT @@version AS version, @@version_comment AS distribution, CURRENT_USER() AS authenticated_account'),'binlog':q("SHOW VARIABLES WHERE Variable_name IN ('log_bin','binlog_format','gtid_mode','binlog_expire_logs_seconds')"),'views':q('SELECT TABLE_SCHEMA,TABLE_NAME,DEFINER,SECURITY_TYPE,IS_UPDATABLE,VIEW_DEFINITION FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA=%s AND TABLE_NAME=%s',(db,view)),'create_view':q('SHOW CREATE VIEW `pf_TickleRight_9210`.`co_eff_monthly_view_new`'),'view_evaluation':q('EXPLAIN SELECT * FROM `pf_TickleRight_9210`.`co_eff_monthly_view_new` LIMIT 0'),'backup_grants':q('SHOW GRANTS FOR CURRENT_USER()'),'engines':q('SELECT TABLE_SCHEMA,ENGINE,COUNT(*) AS tables,SUM(DATA_LENGTH+INDEX_LENGTH) AS bytes FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA IN ('+','.join(['%s']*len(v['BACKUP_LOGICAL_DATABASES'].split(',')))+') GROUP BY TABLE_SCHEMA,ENGINE',v['BACKUP_LOGICAL_DATABASES'].split(','))}
  views=out['mysql']['views']
  if isinstance(views,list) and views:
   definition=views[0].get('VIEW_DEFINITION')
   if definition:out['mysql']['definition_explain']=q('EXPLAIN '+definition)
   definer=views[0]['DEFINER'];user,host=definer.rsplit('@',1)
   out['mysql']['definer_exists']=q('SELECT User,Host FROM mysql.user WHERE User=%s AND Host=%s',(user,host))
   out['mysql']['definer_grants']=q('SHOW GRANTS FOR %s@%s',(user,host))
 c.close()
except Exception as e:out['mysql_error']=fail(e)
print('mysql metadata audit complete',flush=True)
try:
 s=boto3.client('s3',region_name=v['BACKUP_S3_REGION'],aws_access_key_id=v['BACKUP_S3_KEY_ID'],aws_secret_access_key=v['BACKUP_S3_KEY_SECRET'],endpoint_url=v.get('BACKUP_S3_ENDPOINT') or None,config=Config(connect_timeout=10,read_timeout=30,retries={'max_attempts':3}))
 bucket=v['BACKUP_S3_BUCKET'];out['s3']={'bucket':bucket,'prefixes':{},'logical_verification':[]}
 for prefix in [v['BACKUP_S3_LOGICAL_PREFIX']+'/',v['BACKUP_S3_PHYSICAL_PREFIX']+'/']:
  entries=[]
  for page in s.get_paginator('list_objects_v2').paginate(Bucket=bucket,Prefix=prefix):
   entries.extend({'key':x['Key'],'size':x['Size'],'modified':x['LastModified'].isoformat()} for x in page.get('Contents',[]))
  out['s3']['prefixes'][prefix]=entries
  print('S3 inventory',prefix,'objects',len(entries),'bytes',sum(x['size'] for x in entries),flush=True)
 for method in ['get_bucket_versioning','get_bucket_encryption','get_object_lock_configuration']:
  try:out['s3'][method]=getattr(s,method)(Bucket=bucket)
  except Exception as e:out['s3'][method]=fail(e)
 for run in ['2026-10-10_12-00-01','2026-10-11_02-00-00']:
  prefix=v['BACKUP_S3_LOGICAL_PREFIX']+'/'+run+'/'
  record={'run_id':run,'artifacts':[]}
  try:
   manifest=json.loads(s.get_object(Bucket=bucket,Key=prefix+'manifest.json')['Body'].read());completion=json.loads(s.get_object(Bucket=bucket,Key=prefix+'COMPLETED.json')['Body'].read())
   record.update({'manifest':manifest,'completion':completion})
   for x in manifest.get('results',[]):
    if x.get('status')!='success':continue
    key=prefix+Path(x['output_path']).name;h=s.head_object(Bucket=bucket,Key=key,ChecksumMode='ENABLED');item={'key':key,'size_match':h['ContentLength']==x['size_bytes'],'metadata':h.get('Metadata',{}),'checksum_sha256':h.get('ChecksumSHA256'),'payload_verification':'UNVERIFIED'}
    if a.verify_logical_payloads:
     response=s.get_object(Bucket=bucket,Key=key);sha=hashlib.sha256()
     for b in response['Body'].iter_chunks(chunk_size=1024*1024):sha.update(b)
     response['Body'].close();item['payload_verification']='VERIFIED' if sha.hexdigest()==x.get('artifact_sha256') else 'FAILED'
    record['artifacts'].append(item)
   print('logical remote metadata',run,record['manifest'].get('status'),len(record['artifacts']),flush=True)
  except Exception as e:record['error']=fail(e)
  out['s3']['logical_verification'].append(record)
 key=v['BACKUP_S3_PHYSICAL_PREFIX']+'/2026-10-11_02-00-00/PHYSICAL_COMPLETED.json'
 try:out['s3']['physical_completion']=json.loads(s.get_object(Bucket=bucket,Key=key)['Body'].read())
 except Exception as e:out['s3']['physical_completion_error']=fail(e)
except Exception as e:out['s3_error']=fail(e)
# Credential hashes and secret-bearing grants must never enter reports.
def scrub(x):
 if isinstance(x,dict):return {k:scrub(val) for k,val in x.items() if k not in ('ResponseMetadata',)}
 if isinstance(x,list):return [scrub(val) for val in x]
 if isinstance(x,str):return re.sub(r"IDENTIFIED.*",'[authentication clause redacted]',x,flags=re.I)
 return x
output=Path(a.output);output.parent.mkdir(parents=True,exist_ok=True)
fd=os.open(output,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as f:json.dump(scrub(out),f,indent=2,default=str)
print('restricted audit evidence written',output,flush=True)
