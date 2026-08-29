#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Try loading .env if BACKUP_ENV_FILE or .env exists and is readable
ENV_FILE="${BACKUP_ENV_FILE:-$SCRIPT_DIR/.env}"
if [[ -f "$ENV_FILE" && -r "$ENV_FILE" ]]; then
  set +e
  set -a
  # shellcheck disable=SC1090
  source "$ENV_FILE" 2>/dev/null
  set +a
  set -e
fi

DB_USER="${DB_USER:-}"
DB_PASS="${DB_PASS:-}"
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-3306}"
MYSQL_BIN="${MYSQL_BIN:-mysql}"
DB_ENGINE="${DB_ENGINE:-mysql}"

TARGET_DB=""
SHOW_TABLES=false
SHOW_COLUMNS=false
INCLUDE_SYSTEM=false
OUTPUT_JSON=false

usage() {
  cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Checks character set and collation information for databases.

Database Connection Options:
  -u, --user USER         MySQL username (default: \$DB_USER or empty)
  -p, --pass PASS         MySQL password (default: \$DB_PASS or empty)
  -H, --host HOST         MySQL host (default: ${DB_HOST})
  -P, --port PORT         MySQL port (default: ${DB_PORT})

Reporting Options:
  -d, --database DB_NAME  Filter results to a specific database
  -t, --tables            Show table-level collation breakdown
  -c, --columns           Show column-level collation breakdown
  -a, --all               Include system databases (information_schema, performance_schema, mysql, sys)
  -j, --json              Output results in JSON format
  -h, --help              Show this help message and exit

Examples:
  $(basename "$0")                          # Show default collation for all user databases
  $(basename "$0") -u root -p secret        # Connect with credentials
  $(basename "$0") -d pf_central            # Show collation for pf_central database
  $(basename "$0") -t -d pf_central         # Show table collations for pf_central
  $(basename "$0") -c -d pf_central         # Show column collations for pf_central
  $(basename "$0") -a                       # Include system databases
EOF
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -u|--user)
      DB_USER="$2"
      shift 2
      ;;
    -p|--pass)
      DB_PASS="$2"
      shift 2
      ;;
    -H|--host)
      DB_HOST="$2"
      shift 2
      ;;
    -P|--port)
      DB_PORT="$2"
      shift 2
      ;;
    -d|--database)
      TARGET_DB="$2"
      shift 2
      ;;
    -t|--tables)
      SHOW_TABLES=true
      shift
      ;;
    -c|--columns)
      SHOW_COLUMNS=true
      shift
      ;;
    -a|--all)
      INCLUDE_SYSTEM=true
      shift
      ;;
    -j|--json)
      OUTPUT_JSON=true
      shift
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      ;;
  esac
done

if [[ "$DB_ENGINE" != "mysql" && "$DB_ENGINE" != "mariadb" ]]; then
  echo "ERROR: Unsupported DB_ENGINE '$DB_ENGINE'. This script currently supports MySQL / MariaDB." >&2
  exit 1
fi

TMP_CNF="$(mktemp)"
cleanup() {
  rm -f "$TMP_CNF"
}
trap cleanup EXIT

chmod 600 "$TMP_CNF"
cat > "$TMP_CNF" <<EOF
[client]
user=$DB_USER
password=$DB_PASS
host=$DB_HOST
port=$DB_PORT
EOF

# Build system DB exclusion clause
WHERE_CLAUSE="1=1"
if [[ "$INCLUDE_SYSTEM" == false ]]; then
  WHERE_CLAUSE="SCHEMA_NAME NOT IN ('information_schema', 'performance_schema', 'mysql', 'sys')"
fi

if [[ -n "$TARGET_DB" ]]; then
  # Escape single quotes in target database name for safety
  SAFE_TARGET_DB="${TARGET_DB//\'/\'\'}"
  WHERE_CLAUSE="$WHERE_CLAUSE AND SCHEMA_NAME = '$SAFE_TARGET_DB'"
fi

WHERE_CLAUSE_TABLES="${WHERE_CLAUSE//SCHEMA_NAME/TABLE_SCHEMA}"
WHERE_CLAUSE_COLUMNS="${WHERE_CLAUSE//SCHEMA_NAME/TABLE_SCHEMA}"

MYSQL_CMD=("$MYSQL_BIN" "--defaults-extra-file=$TMP_CNF")

if [[ "$OUTPUT_JSON" == true ]]; then
  MYSQL_CMD+=("--json")
fi

echo "=================================================================="
echo " Database Collation Report ($(date '+%Y-%m-%d %H:%M:%S'))"
echo " Host: ${DB_HOST}:${DB_PORT} | User: ${DB_USER:-<default>}"
echo "=================================================================="
echo ""

echo "--- [1] Database Default Character Set & Collation ---"
"${MYSQL_CMD[@]}" -e "
SELECT 
    SCHEMA_NAME AS 'Database', 
    DEFAULT_CHARACTER_SET_NAME AS 'Charset', 
    DEFAULT_COLLATION_NAME AS 'Collation'
FROM information_schema.SCHEMATA
WHERE $WHERE_CLAUSE
ORDER BY SCHEMA_NAME;
"

if [[ "$SHOW_TABLES" == true ]]; then
  echo ""
  echo "--- [2] Table Collations ---"
  "${MYSQL_CMD[@]}" -e "
  SELECT 
      TABLE_SCHEMA AS 'Database',
      TABLE_NAME AS 'Table',
      ENGINE AS 'Engine',
      TABLE_COLLATION AS 'Collation'
  FROM information_schema.TABLES
  WHERE $WHERE_CLAUSE_TABLES AND TABLE_TYPE = 'BASE TABLE'
  ORDER BY TABLE_SCHEMA, TABLE_NAME;
  "
fi

if [[ "$SHOW_COLUMNS" == true ]]; then
  echo ""
  echo "--- [3] Column Collations (Text / String Columns) ---"
  "${MYSQL_CMD[@]}" -e "
  SELECT 
      TABLE_SCHEMA AS 'Database',
      TABLE_NAME AS 'Table',
      COLUMN_NAME AS 'Column',
      DATA_TYPE AS 'Type',
      CHARACTER_SET_NAME AS 'Charset',
      COLLATION_NAME AS 'Collation'
  FROM information_schema.COLUMNS
  WHERE $WHERE_CLAUSE_COLUMNS AND COLLATION_NAME IS NOT NULL
  ORDER BY TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME;
  "
fi

echo ""
EOF
