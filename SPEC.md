# dbq profile specification

This document defines the credential profile format and the procedure for
adding a new database profile **without leaking credentials**.

## File location and permissions

| Item | Path | Mode |
|---|---|---|
| Profile file | `~/.config/dbq/profiles.toml` (override: `$DBQ_PROFILES`) | `600` (enforced - dbq refuses to load otherwise) |
| Directory | `~/.config/dbq/` | `700` |

The profile file is the **only** place credentials live. It is outside every
repository. It must never be copied into git, chat, tickets, or logs.

## File format

```toml
[defaults]                     # optional; applied to every profile
max_rows = 1000                # row cap per query
timeout_seconds = 30           # per-query timeout
readonly = true                # default posture: read-only

[profiles.<name>]
type = "mysql"                 # mysql | postgres | sqlserver | sqlite | oracle
host = "db1.example.com"       # required except sqlite
port = 3306                    # optional; defaults per engine (3306/5432/1433/1521)
user = "agent_ro"
password = "..."               # inline secret (file is chmod 600)
# - OR -
password_env = "DBQ_PASS_X"    # value read from environment, never stored
database = "appdb"             # required: mysql/postgres/sqlserver; oracle = service name
# sid = "ORCL"                 # oracle alternative to database
# path = "/data/dev.db"        # sqlite only
# sslmode = "require"          # postgres only
readonly = true                # default true; false = writable (see below)
max_rows = 500                 # optional per-profile override
timeout_seconds = 15           # optional per-profile override
```

Set `password` **or** `password_env`, never both (dbq rejects both).

### Why TOML

- **Comments** - JSON has none; profile files rely on comments for guidance
- **No YAML footguns** - `no` stays a string (not `false`), no implicit
  type coercion, no indentation surprises; passwords with `:` or `#` are
  safe inside quotes
- **Typed values** - `port = 5432` is an int, `readonly = false` is a bool
  (plain INI would treat everything as a string)
- **Structure** - `[profiles.<name>]` sections map 1:1 to our model
- **Safe quoting** - literal strings `'...'` need no escaping, ideal for
  passwords containing `\` or `"` (dbq picks the quoting form automatically)
- **Ecosystem** - the de-facto config standard (Cargo, pyproject.toml,
  GitHub-centered tooling); BurntSushi/toml is a tiny, stable Go dependency

### Per-type required fields

| type | required | notes |
|---|---|---|
| `mysql` | host, database | user/password as available |
| `postgres` | host, database | `sslmode` optional |
| `sqlserver` | host, database | database = catalog |
| `sqlite` | path | no credentials |
| `oracle` | host + (`database` = service name **or** `sid`) | schema exploration uses `user_*` views of the login user |

## Permission model

Every profile is **read-only by default**. Protection has two layers:

| layer | what it does | mysql | postgres | sqlserver | sqlite | oracle |
|---|---|---|---|---|---|---|
| 1. statement guard | single statement only; read-only keyword allowlist; literals/comments stripped first | yes | yes | yes + full-statement DML/DDL keyword scan | yes | yes |
| 2. engine read-only | database itself rejects writes | `START TRANSACTION READ ONLY` | `BEGIN TRANSACTION READ ONLY` | **not available** | `PRAGMA query_only` | `SET TRANSACTION READ ONLY` |

Plus per-query `max_rows` cap and `timeout_seconds`.

**Read-only DB account (preferred):** if you can create one, use it:

```sql
-- mysql
CREATE USER 'agent_ro'@'%' IDENTIFIED BY '<generate locally>';
GRANT SELECT ON appdb.* TO 'agent_ro'@'%';
-- postgres
CREATE ROLE agent_ro LOGIN PASSWORD '<generate locally>';
GRANT CONNECT ON DATABASE appdb TO agent_ro;
GRANT USAGE ON SCHEMA public TO agent_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO agent_ro;
```

**Admin account in a profile:** allowed, because often no read-only account
exists. Then the two guard layers above are all that stand between the agent
and your data - keep `readonly = true`, and remember sqlserver relies on the
statement guard alone (dbq prints a warning every time).

**Writable profile (`readonly = false`):** only for humans/automation that
genuinely need DML/DDL. The single-statement rule still applies.

## Adding a new profile securely (procedure)

**Option A - interactive wizard (preferred):**

```bash
dbq add
```

Prompts for name/type/host/port/user/database; the password is read from
the terminal **with echo disabled** (or from stdin - never from a command
line argument), then appends the block to the profile file with mode 600.
The wizard refuses duplicate names and insecure existing files.

**Option B - manual edit:**

1. Generate the password **locally**, never paste it to an AI/chat:
   ```bash
   openssl rand -base64 24 | pbcopy
   ```
2. Create the file if it does not exist, with correct modes from the start:
   ```bash
   mkdir -p ~/.config/dbq && chmod 700 ~/.config/dbq
   touch ~/.config/dbq/profiles.toml && chmod 600 ~/.config/dbq/profiles.toml
   ```
3. Edit it with your editor (`vim`/`nano`), add the `[profiles.<name>]` block.
   Do this in a terminal, **not** by asking an AI agent to write it - the
   password would land in the conversation log.
4. Verify:
   ```bash
   dbq list              # profile appears, PASSWORD column shows inline/env
   dbq ping <name>       # connectivity OK
   dbq query <name> "SELECT 1"
   ```
5. Optional hygiene: prefer `password_env` if other people/processes share
   the machine, and export it from a shell rc file that agents do not source.

## Updating an existing profile

```bash
dbq edit <name>
```

Interactive, same rules as the add wizard: every prompt is prefilled with
the current value (Enter keeps it), secrets are read from the terminal with
echo disabled - never from argv. The rewrite touches only the lines whose
value changed; comments, `[defaults]`, other profiles and keys dbq does not
manage (`max_rows`, `timeout_seconds`, ...) are preserved, and the file
stays mode 600.

Manual edit of the file remains supported (same permissions and
never-ask-an-agent rules as above); `dbq edit` is just the friendly path.

## Removing a profile

```bash
dbq rm <name>        # 'dbq remove' is an alias
```

Shows the profile summary and asks for confirmation (default **No** - EOF
or an empty answer aborts, so a stray pipe cannot delete credentials). The
rewrite removes the profile's section including its attached comment lines,
keeps `[defaults]`, other profiles and their comments intact, and stays mode
600. Removing the last profile leaves the file with only the header/defaults
(`dbq add` works again afterwards).

## Never-do list

- Never commit `profiles.toml` (add `profiles.toml` to any global gitignore
  if the file could end up inside a repo by accident).
- Never put credentials in the query, on the command line, or in DSNs
  passed through arguments - dbq reads them from the profile only.
- Never change `readonly` to `false` on a profile whose credentials are
  admin-level without understanding the blast radius.
- Never weaken file modes to make an error go away; fix the secret instead.
