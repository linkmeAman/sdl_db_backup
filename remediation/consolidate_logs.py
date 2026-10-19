#!/usr/bin/env python3
"""Consolidate backup logs without changing immutable history or collector offsets."""
import argparse, ctypes, datetime, fcntl, hashlib, json, os, re, shutil, stat, tempfile
from pathlib import Path
from dotenv import dotenv_values

ROOT=Path(__file__).resolve().parent.parent

def atomic(path,data,mode=0o600):
 path.parent.mkdir(parents=True,exist_ok=True)
 tmp=path.with_name('.'+path.name+'.migration-tmp')
 fd=os.open(tmp,os.O_WRONLY|os.O_CREAT|os.O_EXCL,mode)
 try:
  with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
  os.replace(tmp,path)
 finally:
  if tmp.exists():tmp.unlink()

def exchange(a,b):
 libc=ctypes.CDLL(None,use_errno=True)
 func=libc.renameat2;func.argtypes=[ctypes.c_int,ctypes.c_char_p,ctypes.c_int,ctypes.c_char_p,ctypes.c_uint]
 if func(-100,os.fsencode(a),-100,os.fsencode(b),2):
  errno=ctypes.get_errno();raise OSError(errno,os.strerror(errno))

def inventory(root):
 out={}
 for p in sorted(root.rglob('*')):
  if p.is_symlink():raise RuntimeError('unexpected symlink in source tree: '+str(p))
  if p.is_dir():continue
  info=p.stat()
  if not stat.S_ISREG(info.st_mode):raise RuntimeError('unexpected special file: '+str(p))
  h=hashlib.sha256()
  with p.open('rb') as f:
   for chunk in iter(lambda:f.read(1<<20),b''):h.update(chunk)
  out[str(p.relative_to(root))]={'bytes':info.st_size,'sha256':h.hexdigest(),'mode':stat.S_IMODE(info.st_mode)}
 return out

def archive(source,target):
 expected=inventory(source)
 if target.exists():raise RuntimeError('archive destination exists: '+str(target))
 staging=target.with_name('.'+target.name+'.migration-copy')
 shutil.copytree(source,staging,copy_function=shutil.copy2)
 if inventory(staging)!=expected:raise RuntimeError('archive copy failed checksum/permission comparison')
 os.rename(staging,target);os.chmod(target,0o700)
 link=source.with_name('.'+source.name+'.migration-original')
 if link.exists() or link.is_symlink():raise RuntimeError('unfinished archive migration found')
 link.symlink_to(target,target_is_directory=True)
 exchange(source,link) # source atomically becomes the compatibility link
 if inventory(link)!=expected: # a concurrent writer changed the old archive
  exchange(source,link);link.unlink();raise RuntimeError('archive changed during migration; original restored')
 shutil.rmtree(link) # verified copied original only; backup payloads are never involved
 return {'old_path':str(source),'canonical_path':str(target),'files':len(expected),'bytes':sum(x['bytes'] for x in expected.values())}

def self_test():
 with tempfile.TemporaryDirectory() as name:
  d=Path(name);old=d/'old';new=d/'new';old.mkdir();(old/'run.log').write_text('before\n')
  writer=(old/'run.log').open('a');new.symlink_to(new,target_is_directory=True);exchange(old,new)
  writer.write('after\n');writer.flush();writer.close()
  assert old.is_symlink() and not new.is_symlink() and (old/'run.log').read_text()==(new/'run.log').read_text()=='before\nafter\n'
  source=d/'archive';source.mkdir();(source/'private.log').write_text('private');(source/'private.log').chmod(0o600)
  result=archive(source,new/'diagnostics')
  assert result['files']==1 and source.is_symlink() and (new/'diagnostics').stat().st_mode&0o777==0o700
  assert inventory(new/'diagnostics')['private.log']['mode']==0o600
 print('PASS: atomic directory/link exchange preserves open writers, archived bytes and private permissions')

def main():
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--apply',action='store_true');p.add_argument('--self-test',action='store_true')
 p.add_argument('--env',default=os.environ.get('BACKUP_ENV_FILE',str(ROOT/'.env')))
 p.add_argument('--log-dir',help='New destination; defaults to the configured BACKUP_LOG_DIR (no move)')
 p.add_argument('--journal-dir',default=str(ROOT/'remediation/log-path-migration'))
 a=p.parse_args()
 if a.self_test:self_test();return
 env_file=Path(a.env).absolute();values=dotenv_values(env_file)
 backup_dir=Path(values.get('BACKUP_DIR') or ROOT/'backups').absolute()
 OLD=Path(values.get('BACKUP_LOG_DIR') or backup_dir/'logs').absolute()
 NEW=Path(a.log_dir).absolute() if a.log_dir else OLD
 JOURNAL=Path(a.journal_dir).absolute()
 if OLD==NEW:
  print(json.dumps({'state':'CONFIGURED','log_dir':str(NEW),'lock_file':values.get('BACKUP_LOCK_FILE') or str(NEW/'backup.lock')}));return
 if OLD in NEW.parents or NEW in OLD.parents:raise RuntimeError('log directories must not contain each other')
 lock_file=Path(values.get('BACKUP_LOCK_FILE') or OLD/'backup.lock').absolute()
 if lock_file!=OLD/'backup.lock':raise RuntimeError('custom lock location requires a separate migration plan')
 ARCHIVES=[(s,t) for s,t in [(ROOT/'remediation/oneoff-logs',NEW/'diagnostics'),(ROOT/'.tmp-smoke/logs',NEW/'diagnostics/smoke')] if s.is_dir() and not s.is_symlink()]
 assert OLD.is_dir() and not OLD.is_symlink() and not NEW.exists() and not NEW.is_symlink()
 before=inventory(OLD)
 plan={'runtime':{'old_path':str(OLD),'canonical_path':str(NEW),'files':len(before),'bytes':sum(x['bytes'] for x in before.values())},'archives':[{'old_path':str(s),'canonical_path':str(t),'files':len(inventory(s))} for s,t in ARCHIVES], 'compatibility_links':True,'collector_config_changed':False,'backup_payloads_changed':False}
 print(json.dumps(plan,indent=2),flush=True)
 if not a.apply:return
 JOURNAL.mkdir(mode=0o700,exist_ok=True);os.chmod(JOURNAL,0o700)
 if (JOURNAL/'state.json').exists():raise RuntimeError('migration journal already exists; inspect it before retrying')
 with (JOURNAL/'operation.lock').open('a+') as schedule_lock, (OLD/'backup.lock').open('r+') as backup_lock:
  for f in [schedule_lock,backup_lock]:fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
  prior_pid=re.search(r'pid=(\d+)',backup_lock.read())
  if prior_pid:
   try:os.kill(int(prior_pid.group(1)),0)
   except ProcessLookupError:pass
   else:raise RuntimeError('legacy backup owner remains active')
  original=env_file.read_bytes();info=env_file.stat()
  assert info.st_uid==os.geteuid() and stat.S_IMODE(info.st_mode)==0o600
  before=inventory(OLD);atomic(JOURNAL/'original.env',original)
  state={'state':'PREPARED','at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'plan':plan,'archives':[]}
  atomic(JOURNAL/'state.json',(json.dumps(state,indent=2)+'\n').encode())
  NEW.parent.mkdir(mode=0o755,parents=True,exist_ok=True)
  assert NEW.parent.stat().st_dev==OLD.stat().st_dev
  NEW.symlink_to(NEW,target_is_directory=True)
  try:exchange(OLD,NEW)
  except BaseException:
   NEW.unlink();raise
  state['state']='RUNTIME_MOVED';atomic(JOURNAL/'state.json',(json.dumps(state,indent=2)+'\n').encode())
  assert OLD.is_symlink() and OLD.resolve()==NEW and inventory(NEW)==before
  assert os.stat(NEW/'backup.lock').st_ino==os.fstat(backup_lock.fileno()).st_ino
  for source,target in ARCHIVES:
   state['archives'].append(archive(source,target))
   atomic(JOURNAL/'state.json',(json.dumps(state,indent=2)+'\n').encode())
  assert env_file.read_bytes()==original
  text=original.decode()
  for key,value in {'BACKUP_LOG_DIR':str(NEW),'BACKUP_LOCK_FILE':str(NEW/'backup.lock')}.items():
   text,n=re.subn(r'^'+key+r'=.*$',lambda _:key+'='+json.dumps(value),text,flags=re.M)
   if not n:text+='\n'+key+'='+json.dumps(value)+'\n'
  atomic(env_file,text.encode(),stat.S_IMODE(info.st_mode))
  assert env_file.stat().st_uid==info.st_uid
  assert all((NEW/key).is_file() and hashlib.sha256((NEW/key).read_bytes()).hexdigest()==value['sha256'] for key,value in before.items())
  state['state']='APPLIED';state['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();state['log_dir']=str(NEW)
  atomic(JOURNAL/'state.json',(json.dumps(state,indent=2)+'\n').encode())
 print(json.dumps({'state':'APPLIED','log_dir':str(NEW),'lock_file':str(NEW/'backup.lock'),'runtime_files_preserved':len(before),'archives':state['archives'],'collector_old_path_resolves_to_canonical':OLD.resolve()==NEW},indent=2))

if __name__=='__main__':main()
