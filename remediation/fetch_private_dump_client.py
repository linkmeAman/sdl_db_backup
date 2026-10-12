"""Download/extract an official client privately; never install/replace system packages."""
import gzip,hashlib,re,subprocess,urllib.request,json
from pathlib import Path
base='https://repo.mysql.com/apt/ubuntu/';index=base+'dists/jammy/mysql-8.4-lts/binary-amd64/Packages.gz';root=Path('/tmp/sdl-private-mysql84');root.mkdir(exist_ok=True)
with urllib.request.urlopen(index,timeout=30) as r:data=gzip.decompress(r.read()).decode()
fields=None
for block in data.split('\n\n'):
 rows=dict(line.split(': ',1) for line in block.splitlines() if ': ' in line and not line.startswith(' '))
 if rows.get('Package')=='mysql-community-client-core':fields=rows;break
if not fields:raise SystemExit('Official client package not found')
name=fields['Filename'];url=base+name;package=root/Path(name).name
with urllib.request.urlopen(url,timeout=30) as r:body=r.read()
if hashlib.sha256(body).hexdigest()!=fields['SHA256']:raise SystemExit('Package SHA256 mismatch')
package.write_bytes(body);subprocess.run(['dpkg-deb','-x',str(package),str(root/'extracted')],check=True)
proof={'origin':url,'version':fields['Version'],'package_sha256':fields['SHA256'],'verification':'HTTPS official repository and package SHA256; package signature not independently verified','private_client':str(root/'extracted/usr/bin/mysqldump')};(root/'provenance.json').write_text(json.dumps(proof,indent=2));print(json.dumps(proof));subprocess.run([proof['private_client'],'--version'],check=True)
