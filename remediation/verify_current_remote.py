import boto3,json,hashlib,datetime
from dotenv import dotenv_values
from botocore.config import Config
from pathlib import Path
v=dotenv_values('.env');s=boto3.client('s3',region_name=v['BACKUP_S3_REGION'],aws_access_key_id=v['BACKUP_S3_KEY_ID'],aws_secret_access_key=v['BACKUP_S3_KEY_SECRET'],config=Config(connect_timeout=10,read_timeout=30,retries={'max_attempts':3}));bucket=v['BACKUP_S3_BUCKET'];prefix='logical/2026-10-12_12-00-01/'
m=json.loads(s.get_object(Bucket=bucket,Key=prefix+'manifest.json')['Body'].read());out={'run_id':m['run_id'],'manifest_status':m['status'],'observed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'artifacts':[]}
for x in m['results']:
 if x['status']!='success':continue
 key=prefix+Path(x['output_path']).name;resp=s.get_object(Bucket=bucket,Key=key);h=hashlib.sha256();n=0
 for b in resp['Body'].iter_chunks(chunk_size=1024*1024):h.update(b);n+=len(b)
 out['artifacts'].append({'key':key,'bytes':n,'sha256_match':h.hexdigest()==x['artifact_sha256'],'size_match':n==x['size_bytes']});print(key,'VERIFIED' if out['artifacts'][-1]['sha256_match'] else 'FAILED',flush=True)
try:
 versions=s.list_object_versions(Bucket=bucket,Prefix='logical/2026-10-10_12-00-01/',MaxKeys=10);out['oct10_versions']=[{'key':x['Key'],'version_id':x['VersionId'],'size':x['Size']} for x in versions.get('Versions',[])]
except Exception as e:out['oct10_versions_error']=getattr(e,'response',{}).get('Error',{}).get('Code',type(e).__name__)
Path('/tmp/sdl-current-remote-checksums.json').write_text(json.dumps(out,indent=2));print('read-only verification complete',flush=True)
