import json,pymysql
from dotenv import dotenv_values
v=dotenv_values('.env');c=pymysql.connect(host=v['DB_HOST'],user=v['DB_USER'],password=v['DB_PASS'],port=int(v['DB_PORT']),read_timeout=15,connect_timeout=10)
with c.cursor() as cur:
 for label,sql in [('show_fields','SHOW FIELDS FROM `pf_TickleRight_9210`.`co_eff_monthly_view_new`'),('select_zero_rows','SELECT * FROM `pf_TickleRight_9210`.`co_eff_monthly_view_new` LIMIT 0'),('sql_mode','SELECT @@SESSION.sql_mode'),('source_identity','SELECT @@server_uuid')]:
  try:
   cur.execute(sql);print(label, 'VERIFIED' if label in ['show_fields','select_zero_rows'] else cur.fetchall())
  except pymysql.MySQLError as e:print(label,'FAILED',e.args[0],e.args[1])
c.close()
