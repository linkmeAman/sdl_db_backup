"""Read-only verification of the existing local Prometheus backup metrics/rules."""
import datetime
import json
from pathlib import Path
import urllib.parse
import urllib.request

root = Path(__file__).resolve().parent.parent

def request(path):
    with urllib.request.urlopen('http://127.0.0.1:9090' + path, timeout=10) as r:
        result = json.load(r)
    if result.get('status') != 'success':
        raise RuntimeError('Prometheus API query failed')
    return result['data']

metrics = {}
for name in ['backup_storage_auto_cleanup_enabled', 'backup_storage_manager_critical',
             'backup_storage_capacity_sufficient', 'backup_storage_available_bytes',
             'backup_storage_required_bytes', 'backup_last_status']:
    data = request('/api/v1/query?' + urllib.parse.urlencode({'query': name}))
    metrics[name] = [row['value'][1] for row in data['result']]
rules = request('/api/v1/rules')
loaded = any(rule.get('name') == 'SDLBackupStorageRecoveryCritical'
             for group in rules['groups'] for rule in group.get('rules', []))
alertmanagers = request('/api/v1/alertmanagers')
out = {'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
       'scraped_metrics': metrics, 'storage_rule_loaded': loaded,
       'active_alertmanager_count': len(alertmanagers.get('activeAlertmanagers', [])),
       'notification_delivery': 'UNVERIFIED',
       'rule_installation': 'VERIFIED' if loaded else 'BLOCKED_OPERATOR_ROOT_REQUIRED'}
p = root / 'remediation/storage-monitoring-proof.json'
p.write_text(json.dumps(out, indent=2) + '\n')
p.chmod(0o600)
print(json.dumps(out, indent=2))
