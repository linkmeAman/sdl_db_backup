"""Independent read-only checks of the newly committed remote backup."""
import json,hashlib,datetime,os
from pathlib import Path
import boto3,pymysql
from dotenv import dotenv_values
from botocore.config import Config
v=dotenv_values('.env');bucket=v['BACKUP_S3_BUCKET'];prefix='recovery-hold/logical/2026-10-12_14-23-04/'
s=boto3.client('s3',region_name=v['BACKUP_S3_REGION'],aws_access_key_id=v['BACKUP_S3_KEY_ID'],aws_secret_access_key=v['BACKUP_S3_KEY_SECRET'],config=Config(connect_timeout=10,read_timeout=30,retries={'max_attempts':3}))
marker=json.loads(s.get_object(Bucket=bucket,Key=prefix+'COMPLETED.json')['Body'].read());manifest=json.loads(s.get_object(Bucket=bucket,Key=prefix+'manifest.json')['Body'].read())
required=v['BACKUP_LOGICAL_DATABASES'].split(',');out={'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'run_id':marker['run_id'],'marker_status':marker.get('status'),'marker_version':marker.get('format_version'),'expected_databases_match':sorted(marker.get('expected_databases',[]))==sorted(required),'manifest_status':manifest.get('status'),'six_successes':len(manifest['results'])==6 and all(x['status']=='success' for x in manifest['results']),'consistency_method':manifest.get('consistency_method'),'expected_object_counts':{x['name']:{k:len(names) for k,names in x.get('expected_objects',{}).items()} for x in manifest['results']},'artifacts':[],'restore_state':'UNVERIFIED'}
for artifact in marker['artifacts']:
 resp=s.get_object(Bucket=bucket,Key=prefix+artifact['key']);sha=hashlib.sha256();count=0
 for data in resp['Body'].iter_chunks(chunk_size=1024*1024):sha.update(data);count+=len(data)
 resp['Body'].close();item={'key':artifact['key'],'size':count,'size_match':count==artifact['size_bytes'],'sha256_match':sha.hexdigest()==artifact['sha256'],'server_side_encryption':resp.get('ServerSideEncryption'),'sse_kms_key_present':bool(resp.get('SSEKMSKeyId'))};out['artifacts'].append(item);print('independent S3 verification',item,flush=True)
c=pymysql.connect(host=v['DB_HOST'],port=int(v['DB_PORT']),user=v['DB_USER'],password=v['DB_PASS'],connect_timeout=10,read_timeout=10)
with c.cursor() as cur:
 cur.execute('SELECT @@GLOBAL.max_execution_time');out['production_global_max_execution_time_ms']=cur.fetchone()[0]
 cur.execute('SELECT DEFINER,SECURITY_TYPE FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA=%s AND TABLE_NAME=%s',('pf_TickleRight_9210','co_eff_monthly_view_new'));out['view_security']=cur.fetchone()
c.close();out['capture_and_remote_integrity_verdict']='VERIFIED' if out['marker_status']=='COMPLETE' and out['expected_databases_match'] and out['six_successes'] and all(x['sha256_match'] and x['size_match'] for x in out['artifacts']) else 'FAILED'
p=Path('remediation/final-backup-proof.json');p.write_text(json.dumps(out,indent=2));p.chmod(0o600);print(json.dumps({k:x for k,x in out.items() if k not in ['expected_object_counts','artifacts']}),flush=True)
