#!/usr/bin/env bash
#
# Conformance suite TEMPLATE — from claude-workflow/templates/conformance-suite.
# Copy to <design>/verify-operations.sh, replace SCHEMA with your schema name,
# and fill the four marked sections. Worked examples: ep2.0/wallet and
# ep2.0/leaderboard (the latter is the origin of every helper here).
#
# What a suite is: the operations the design must be able to PERFORM, each
# with an assertion about the state afterwards, with every INVARIANT
# re-checked after every operation, and then MUTANTS that break things on
# purpose and require that an invariant names the breakage. A check that
# cannot fail is decoration; the census at the end enforces that every
# invariant branch has a mutant that names it alone.
#
# Header: say what this suite does NOT cover, and must not be read as covering.
#   - [fill in]

set -Eeuo pipefail

SCRIPT_PATH="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
DSN="${DSN:-postgresql://postgres:postgres@localhost:5432/postgres}"
export PGOPTIONS='-c client_min_messages=warning'
PSQL="psql $DSN -qX -v ON_ERROR_STOP=1"
# A mutation is what a bug does behind the guard's back: it runs with the
# registry's guard trigger off — and nothing else off — so the INVARIANTS (not
# the trigger) are what must catch it.
GUARD_OFF="DO \$\$ DECLARE t text; BEGIN FOR t IN SELECT format('%I.%I', n.nspname, c.relname) FROM pg_trigger g JOIN pg_class c ON c.oid = g.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE g.tgname = 'trg_append_only' LOOP EXECUTE format('ALTER TABLE %s DISABLE TRIGGER trg_append_only', t); END LOOP; END \$\$; "
GUARD_ON="DO \$\$ DECLARE t text; BEGIN FOR t IN SELECT format('%I.%I', n.nspname, c.relname) FROM pg_trigger g JOIN pg_class c ON c.oid = g.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE g.tgname = 'trg_append_only' LOOP EXECUTE format('ALTER TABLE %s ENABLE ALWAYS TRIGGER trg_append_only', t); END LOOP; END \$\$; "
# rpsql -c "<sql>": the guard off, the statement, the guard on — one transaction,
# so a refused mutation leaves the guard armed. Foreign keys and CHECKs stay on:
# a mutant is what a bug does behind the guard, not behind the database.
rpsql() { $PSQL -c "$GUARD_OFF $2 $GUARD_ON"; }

pass=0
fail=0

die() { echo "ERROR: $*" >&2; exit 1; }
trap 'die "line $LINENO: $BASH_COMMAND"' ERR

command -v psql >/dev/null || die "psql not found"
$PSQL -tAc 'SELECT 1' >/dev/null 2>&1 || die "cannot connect to $DSN"

# ── the database under test ─────────────────────────────────────────────────
$PSQL -f "$SCRIPT_PATH/sql/drop_schemas.sql" >/dev/null
$PSQL -f "$SCRIPT_PATH/sql/schemas.sql" >/dev/null

# ── invariants ──────────────────────────────────────────────────────────────
# Re-checked after every operation. Each returns rows only when it is VIOLATED,
# so an empty result is the healthy one and a new violation names itself.

# ── 1. INVARIANTS ─────────────────────────────────────────────────────────────
# One UNION ALL block; every branch is  SELECT '<message>: ' || <key> FROM …
# and the message text is what a mutant's fragment must name. A branch that
# skips rows (e.g. after a retention drop) says so by name in the message.
INVARIANTS=$(cat <<'SQL'
SELECT 'example invariant that never fires: ' || 'x' WHERE false
SQL
)

check_invariants() {
  local broken
  broken=$($PSQL -tA -c "$INVARIANTS" 2>&1 | sed '/^$/d') || true
  if [ -n "$broken" ]; then
    printf '     !! INVARIANT BROKEN by "%s"\n' "$1"; printf '        %s\n' "$broken" | head -12; fail=$((fail+1)); return 1
  fi
  return 0
}

# ── the three helpers: op / refuse / mutant ──────────────────────────────────
op() {
  local out got
  if ! out=$($PSQL -c "$2" 2>&1); then
    printf '  %-66s !! REFUSED: %s\n' "$1" \
           "$(grep -m1 -oE 'ERROR:.*' <<<"$out" | cut -c1-70)"
    fail=$((fail+1)); return
  fi
  got=$($PSQL -tA -c "SELECT CASE WHEN ($3) THEN 'yes' ELSE 'NO' END;" 2>&1)
  if [ "$got" != "yes" ]; then
    printf '  %-66s !! COMMITTED BUT WRONG STATE\n' "$1"
    fail=$((fail+1)); return
  fi
  check_invariants "$1" || return 0
  printf '  %-66s ok\n' "$1"; pass=$((pass+1))
}

# refuse <name> <expected error fragment> <sql>
refuse() {
  local out
  if out=$($PSQL -c "$3" 2>&1); then
    printf '  %-66s !! ALLOWED\n' "$1"; fail=$((fail+1)); return
  fi
  if grep -qF "$2" <<<"$out"; then
    printf '  %-66s refused by %s\n' "$1" "$2"; pass=$((pass+1))
  else
    printf '  %-66s !! WRONG ERROR: %s\n' "$1" \
           "$(grep -m1 -oE 'ERROR:.*' <<<"$out" | cut -c1-60)"
    fail=$((fail+1))
  fi
}

# mutant <name> <sql> <expected invariant fragment>
mutant() {
  local broken
  rpsql -c "$2" >/dev/null 2>&1 || { printf '  %-66s !! MUTATION REFUSED (not a mutation)\n' "$1"; fail=$((fail+1)); return; }
  broken=$($PSQL -tA -c "$INVARIANTS" 2>&1 | sed '/^$/d')
  if grep -qF "$3" <<<"$broken"; then
    printf '  %-66s caught: %s\n' "$1" "$(grep -m1 -F "$3" <<<"$broken" | cut -c1-40)"; pass=$((pass+1))
  else
    printf '  %-66s !! MUTANT SURVIVED\n' "$1"; fail=$((fail+1))
  fi
}


# ── 2. SCAFFOLDING: the reference operations ─────────────────────────────────
# NOT part of the design. What the service layer will have to do, written as
# SQL functions in `public` so the suite can call them. Rules learned:
#   - lock the row that serialises (the board, the account) before deciding;
#   - a value the registry requires to be monotone is taken AFTER the lock
#     with clock_timestamp(), never now() (transaction start runs backwards
#     under contention);
#   - a correction recomputes the one thing it changed, never the world;
#   - records the invariants compare against are one per subject (partial
#     unique index), have a DDL shape, and are written by the operation.
$PSQL >/dev/null <<'SQL'
-- The registry, executed (D23). This is the reference for the trigger step 3
-- generates: every changed column must have a rule and the rule must hold;
-- a delete needs the table to be deletable. AFTER ROW, so a CHECK or FK
-- error keeps precedence. Mutants run with triggers off — they are what a
-- superuser or a bug does behind the guard — and the invariants must catch
-- them; the operations run under it.
CREATE FUNCTION append_only_guard() RETURNS trigger AS $$
DECLARE r record; rule text; ok boolean;
BEGIN
  IF TG_OP = 'DELETE' THEN
    SELECT delete_when INTO rule FROM SCHEMA.append_only WHERE table_name = TG_TABLE_NAME;
    IF rule IS NULL THEN RAISE EXCEPTION 'append_only: rows of % are never deleted', TG_TABLE_NAME; END IF;
    EXECUTE format('SELECT coalesce((%s), false) FROM (SELECT $1 AS "OLD") t', replace(rule, 'OLD.', '("OLD").')) INTO ok USING OLD;
    IF NOT ok THEN RAISE EXCEPTION 'append_only: this row of % may not be deleted now', TG_TABLE_NAME; END IF;
    RETURN OLD;
  END IF;
  FOR r IN SELECT n.key AS col FROM jsonb_each(to_jsonb(NEW)) n JOIN jsonb_each(to_jsonb(OLD)) o ON o.key = n.key
            WHERE n.value IS DISTINCT FROM o.value LOOP
    SELECT allowed_when INTO rule FROM SCHEMA.append_only_column WHERE table_name = TG_TABLE_NAME AND column_name = r.col;
    IF rule IS NULL THEN RAISE EXCEPTION 'append_only: %.% never changes', TG_TABLE_NAME, r.col; END IF;
    EXECUTE format('SELECT coalesce((%s), false) FROM (SELECT $1 AS "OLD", $2 AS "NEW") t',
                   replace(replace(rule, 'OLD.', '("OLD").'), 'NEW.', '("NEW").')) INTO ok USING OLD, NEW;
    IF NOT ok THEN RAISE EXCEPTION 'append_only: %.% may not change this way (% -> %)', TG_TABLE_NAME, r.col,
      to_jsonb(OLD) -> r.col, to_jsonb(NEW) -> r.col; END IF;
  END LOOP;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
-- D4 (r3.12): a reading's placings are written before its record; none after.
CREATE FUNCTION placing_sealed_guard() RETURNS trigger AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM leaderboard.audit x WHERE x.action = 'settlement' AND x.entity_key = NEW.settlement_key) THEN
    RAISE EXCEPTION 'append_only: placing % arrives after its reading''s record', NEW.key;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_placing_sealed BEFORE INSERT ON leaderboard.placing FOR EACH ROW EXECUTE FUNCTION placing_sealed_guard();
ALTER TABLE leaderboard.placing ENABLE ALWAYS TRIGGER trg_placing_sealed;

CREATE FUNCTION append_only_no_truncate() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'append_only: % is never truncated', TG_TABLE_NAME; END $$ LANGUAGE plpgsql;
DO $$ DECLARE t text; BEGIN
  FOR t IN SELECT table_name FROM SCHEMA.append_only LOOP
    EXECUTE format('CREATE TRIGGER trg_append_only AFTER UPDATE OR DELETE ON SCHEMA.%I FOR EACH ROW EXECUTE FUNCTION append_only_guard()', t);
    -- a row guard protects rows; TRUNCATE removes them without one (r3.6)
    EXECUTE format('CREATE TRIGGER trg_append_only_truncate BEFORE TRUNCATE ON SCHEMA.%I FOR EACH STATEMENT EXECUTE FUNCTION append_only_no_truncate()', t);
    EXECUTE format('ALTER TABLE SCHEMA.%I ENABLE ALWAYS TRIGGER trg_append_only, ENABLE ALWAYS TRIGGER trg_append_only_truncate', t);
  END LOOP;
END $$;


SQL

# ── 3. ESTATE + OPERATIONS ───────────────────────────────────────────────────
# Fixed keys (uuids you can grep for), a small world that has every shape
# the decisions talk about — including the empty board, the two-board
# competition, the shared tie — so the invariants run on the shape that
# exposes the hole. Then ops, refusals, mutants, in the order the story
# happens. Age time behind the guard (rpsql), moving the whole fact.

# ── 4. CONCURRENCY ───────────────────────────────────────────────────────────
# From round 3: N producers, a correction racing them, a reading taken
# mid-run; reproduce the reading at its fence and compare the aggregate with
# the fold afterwards. See templates/conformance-suite/load-harness.md.

# ── the census: every invariant branch has a mutant that names it ───────────
census=$(python3 - "$0" <<'CENSUS'
import re, sys
s = open(sys.argv[1]).read()
inv = [m.replace("''", "'").rstrip(': ') for m in re.findall(r"^SELECT '((?:[^']|'')+?)' \|\|", s, re.M)]
frags = re.findall(r'^mutant "[^"]*" "(?:[^"\\]|\\.)*" "([^"]+)"', s, re.M | re.S)
bad = [i for i in inv if not any(f in i for f in frags)]
vague = [f for f in frags if sum(1 for i in inv if f in i) > 1]
reg = len(re.findall(r'^refuse "REGISTRY:', s, re.M))
print(f"{len(inv)} invariant branches, {len(frags)} mutants, {reg} REGISTRY refusals" + (f"; NO MUTANT: {bad}" if bad else "") + (f"; AMBIGUOUS: {vague}" if vague else ""))
sys.exit(1 if (bad or vague) else 0)
CENSUS
) && { printf '  %-66s %s\n' "census: every invariant branch has a mutant" "$census"; pass=$((pass+1)); } \
  || { printf '  %-66s !! %s\n' "census: every invariant branch has a mutant" "$census"; fail=$((fail+1)); }

echo
echo "  $pass operations work, $fail do NOT"
[ "$fail" -eq 0 ] || exit 1
