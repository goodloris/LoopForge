#!/usr/bin/env bash
# Create .env with unique local secrets, or bring an existing one up to date
# without rotating anything. LOOPFORGE_DATABASE picks the database of a new
# installation: postgres (default) or mysql. An existing .env without that key
# is a MySQL installation from before PostgreSQL became the default and keeps
# MySQL. Editing LOOPFORGE_DATABASE in .env and rerunning rewrites only the
# DSNs whose dialect no longer matches it.
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import os,re,secrets,pathlib,shlex,sys
p=pathlib.Path('.env')
data_dir=os.path.abspath(os.environ.get('LOOPFORGE_DATA_DIR','.data'))

def dsns(db,password,mysql_port,postgres_port):
 if db=='postgres':
  return (f'postgres://loopforge:{password}@postgres:5432/loopforge?sslmode=disable',
          f'postgres://loopforge:{password}@127.0.0.1:{postgres_port}/loopforge?sslmode=disable')
 return (f'loopforge:{password}@tcp(mysql:3306)/loopforge',f'loopforge:{password}@tcp(127.0.0.1:{mysql_port})/loopforge')

def is_postgres(dsn): return dsn.lower().startswith(('postgres://','postgresql://'))

def check(db):
 if db not in ('postgres','mysql'): sys.exit(f'LOOPFORGE_DATABASE must be postgres or mysql, not {db!r}')
 return db

if not p.exists():
 db=check(os.environ.get('LOOPFORGE_DATABASE','postgres'))
 password=secrets.token_hex(24)
 http_port=os.environ.get('AGENTFLOW_HTTP_PORT','8090');mysql_port=os.environ.get('LOOPFORGE_MYSQL_PORT','33315');postgres_port=os.environ.get('LOOPFORGE_POSTGRES_PORT','35432')
 compose_dsn,host_dsn=dsns(db,password,mysql_port,postgres_port)
 data={'LOOPFORGE_DATABASE':db,'LOOPFORGE_DB_PASSWORD':password,'LOOPFORGE_DB_ROOT_PASSWORD':secrets.token_hex(24),'LOOPFORGE_POSTGRES_PORT':postgres_port,'LOOPFORGE_MYSQL_PORT':mysql_port,'LOOPFORGE_COMPOSE_DSN':compose_dsn,'COMPOSE_PROFILES':db,'COMPOSE_PROJECT_NAME':os.environ.get('COMPOSE_PROJECT_NAME','loopforge'),'AGENTFLOW_HTTP_PORT':http_port,'AGENTFLOW_DATABASE_DSN':host_dsn,'AGENTFLOW_SESSION_SECRET':secrets.token_hex(32),'AGENTFLOW_AUTH_REQUIRED':'true','LOOPFORGE_DATA_DIR':data_dir,'LOOPFORGE_BIND':os.environ.get('LOOPFORGE_BIND','0.0.0.0'),'LOOPFORGE_PUBLIC_URL':os.environ.get('LOOPFORGE_PUBLIC_URL',''),'LOOPFORGE_IMAGE':os.environ.get('LOOPFORGE_IMAGE',''),'AGENTFLOW_GITHUB_CLIENT_ID':os.environ.get('AGENTFLOW_GITHUB_CLIENT_ID','')}
 fd=os.open(str(p),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f: f.write(''.join(f'{k}={shlex.quote(v)}\n' for k,v in data.items()))
 print(f'Created .env with unique local secrets ({db})')
 raise SystemExit(0)

text=p.read_text()
def stored(name):
 matches=re.findall(r'(?m)^%s=(.*)$'%re.escape(name),text)
 if not matches: return None
 try: return ' '.join(shlex.split(matches[-1]))
 except ValueError: return matches[-1].strip()
updates={}
if stored('LOOPFORGE_DATA_DIR') is None:
 updates['LOOPFORGE_DATA_DIR']=data_dir
db=stored('LOOPFORGE_DATABASE')
if not db:
 db='mysql'
 updates['LOOPFORGE_DATABASE']=db
 print('Existing MySQL installation: keeping MySQL (LOOPFORGE_DATABASE=mysql added to .env).')
 print('  New installations default to PostgreSQL. There is no MySQL to PostgreSQL migration; see docs/deployment.md.')
check(db)
password=stored('LOOPFORGE_DB_PASSWORD')
if not password: sys.exit('.env has no LOOPFORGE_DB_PASSWORD; restore it or move .env away to create a new one')
compose_dsn,host_dsn=dsns(db,password,stored('LOOPFORGE_MYSQL_PORT') or '33315',stored('LOOPFORGE_POSTGRES_PORT') or '35432')
current=stored('LOOPFORGE_COMPOSE_DSN')
if current is None or is_postgres(current)!=(db=='postgres'):
 updates['LOOPFORGE_COMPOSE_DSN']=compose_dsn
# Plain `docker compose up/ps/down` include the database through this, as
# they did before it had a profile. A --profile flag replaces the list.
current=stored('COMPOSE_PROFILES')
if current is None or (current in ('postgres','mysql') and current!=db):
 updates['COMPOSE_PROFILES']=db
current=stored('AGENTFLOW_DATABASE_DSN')
# A MySQL .env from before this key keeps its AGENTFLOW_MYSQL_DSN, which the platform still reads.
if (current is None and not (db=='mysql' and stored('AGENTFLOW_MYSQL_DSN'))) or (current is not None and is_postgres(current)!=(db=='postgres')):
 updates['AGENTFLOW_DATABASE_DSN']=host_dsn
if not updates:
 print('Keeping existing .env')
 raise SystemExit(0)
for k,v in updates.items():
 line=f'{k}={shlex.quote(v)}'
 text,count=re.subn(r'(?m)^%s=.*$'%re.escape(k),lambda m:line,text)
 if not count: text=text.rstrip('\n')+'\n'+line+'\n'
p.write_text(text)
print('Updated .env: '+', '.join(updates))
PY
