"""Executes ONLY the operator-approved definer correction with drift checks."""
import json,re,hashlib,datetime,os
from pathlib import Path
import pymysql
from dotenv import dotenv_values
v=dotenv_values('.env');audit=json.load(open('/tmp/sdl-recovery-audit-live.json'));prior=audit['mysql']['create_view'][0];original=prior['Create View'];fixed=re.sub(r'DEFINER=`developer`@`localhost`','DEFINER=`developer`@`%`',original)
if fixed==original:raise SystemExit('No reviewed definer change available')
c=pymysql.connect(host=v['DB_HOST'],port=int(v['DB_PORT']),user=v['DB_USER'],password=v['DB_PASS'],connect_timeout=10,read_timeout=30,cursorclass=pymysql.cursors.DictCursor)
proof={'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'object':'pf_TickleRight_9210.co_eff_monthly_view_new','old_definer':'developer@localhost','new_definer':'developer@%','status':'FAILED','rollback_applied':False}
with c.cursor() as cur:
 cur.execute('SHOW CREATE VIEW `pf_TickleRight_9210`.`co_eff_monthly_view_new`');current=cur.fetchone()
 if current['Create View']!=original:
  cur.execute('SELECT VIEW_DEFINITION,DEFINER,SECURITY_TYPE FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA=%s AND TABLE_NAME=%s',('pf_TickleRight_9210','co_eff_monthly_view_new'));before=cur.fetchone()
  if before['VIEW_DEFINITION']!=audit['mysql']['views'][0]['VIEW_DEFINITION'] or before['DEFINER']!='developer@localhost' or before['SECURITY_TYPE']!='DEFINER':raise SystemExit('Production view business definition or security drifted since review; no change made')
 cur.execute("SELECT User,Host FROM mysql.user WHERE User='developer' AND Host='%'")
 if not cur.fetchone():raise SystemExit('Approved destination account is missing; no change made')
 cur.execute('USE `pf_TickleRight_9210`')
 charset=prior['character_set_client'];collation=prior['collation_connection']
 if not re.fullmatch(r'[a-zA-Z0-9_]+',charset) or not re.fullmatch(r'[a-zA-Z0-9_]+',collation):raise SystemExit('Invalid charset metadata')
 cur.execute('SET NAMES '+charset+' COLLATE '+collation)
 cur.execute(fixed.replace('CREATE ','ALTER ',1))
 try:
  cur.execute('SHOW FIELDS FROM `co_eff_monthly_view_new`');proof['field_count']=len(cur.fetchall())
  cur.execute('SELECT * FROM `co_eff_monthly_view_new` LIMIT 0');cur.fetchall()
  cur.execute('SHOW CREATE VIEW `co_eff_monthly_view_new`');updated=cur.fetchone()['Create View']
  cur.execute('SELECT VIEW_DEFINITION,DEFINER,SECURITY_TYPE FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA=%s AND TABLE_NAME=%s',('pf_TickleRight_9210','co_eff_monthly_view_new'));observed=cur.fetchone()
  prior_definition=audit['mysql']['views'][0]['VIEW_DEFINITION']
  if observed['VIEW_DEFINITION']!=prior_definition or observed['DEFINER']!='developer@%' or observed['SECURITY_TYPE']!='DEFINER':
   Path('/tmp/sdl-view-canonical-drift.json').write_text(json.dumps({'definition':observed['VIEW_DEFINITION'],'prior_definition':prior_definition}))
   raise RuntimeError('Unexpected change to query or security semantics')
  proof['definition_unchanged_except_definer']=True;proof['status']='VERIFIED'
 except Exception:
  cur.execute(original.replace('CREATE ','ALTER ',1));proof['rollback_applied']=True
  raise
 cur.execute('SELECT TABLE_SCHEMA,TABLE_NAME FROM INFORMATION_SCHEMA.VIEWS WHERE TABLE_SCHEMA IN ('+','.join(['%s']*len(v['BACKUP_LOGICAL_DATABASES'].split(',')))+')',v['BACKUP_LOGICAL_DATABASES'].split(','))
 views=cur.fetchall();proof['required_view_checks']=len(views);proof['failed_views']=[]
 for view in views:
  name='`'+view['TABLE_SCHEMA'].replace('`','``')+'`.`'+view['TABLE_NAME'].replace('`','``')+'`'
  try:cur.execute('SHOW FIELDS FROM '+name);cur.fetchall()
  except pymysql.MySQLError as e:proof['failed_views'].append({'schema':view['TABLE_SCHEMA'],'view':view['TABLE_NAME'],'code':e.args[0]})
c.close();p=Path('/tmp/sdl-view-fix-proof.json');p.write_text(json.dumps(proof,indent=2));p.chmod(0o600);print(json.dumps(proof),flush=True)
