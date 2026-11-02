# Controller API

Open the API address in a web browser to browse workload nodes, stacks, and containers. Select a container to see its details and live logs. Refresh reloads the current view and reconnects the log stream.

The API reads `/app/config/nodes.yaml`, reconciles configured hosts into `/app/data/controller.sqlite`, and checks whether each SSH TCP port accepts a connection at startup. Unreachable hosts are recorded and do not prevent the API from starting. A TCP check does not verify SSH authentication or PostgreSQL. Hosts removed from the configuration and their associated local records are removed at startup; remote workloads, databases, and backup files are left in place. Connectivity is checked again on restart.

`GET /health` returns the process health; `GET /nodes` returns the last recorded status of configured nodes. On startup, the API initializes database hosts over SSH and records commissioning status in `/nodes`. It generates the PostgreSQL master password once and encrypts it in SQLite with `DB_ENCRYPTION_KEY`. The API trusts and records each SSH host key on first connection, then rejects changed keys. The first connection is not independently authenticated, so it must occur on a trusted network. SSH host keys are stored in SQLite. Database hosts must have the current Compose file installed. Existing database volumes or `.env` files without a stored credential require manual investigation. The configured PostgreSQL port must be reachable from workloads.

## Deploy databases

`POST /deploy` accepts `multipart/form-data` with `forearm` and `compose` file parts for stack deployments. The `env` JSON file part is optional and defaults to an empty environment. Send GitHub secrets and programmatically generated values, including image references, in the optional `secrets` JSON part; it overrides matching values in `env`. Use `env` only for user-defined settings. Values in both parts may be strings, numbers, or booleans; numbers and booleans are converted to strings. Multiline strings are supported. Both parts are written to the stack `.env` on the host, so the secrets part is not separate storage. Database-only deployments can still send the `.forearm` JSON directly. The API validates stack nodes as workload hosts and database nodes as database hosts. The stack job writes `docker-compose.yml` and the merged `.env` under `~/stacks/<name>`, then starts the Compose project and waits for readiness. If deployment fails, it attempts to restore the previous configuration and stack. `GET /stacks` lists recorded stacks; `GET /stacks/{id}` retrieves a record, live Compose container status over SSH, and databases associated in the `.forearm` deployment. A valid request returns HTTP 201 and a list of stack and database jobs; use `GET /jobs` or `GET /jobs/{id}` to check their results. A restart marks unfinished jobs as failed.

Each database requires `name`, `user`, and `node`. Names and users must be lowercase PostgreSQL identifiers containing letters, digits, and underscores, start with a letter or underscore, and be at most 63 characters. Optional `backup_schedule` settings configure `frequency` (`hourly`, `daily`, or `weekly`), `time` (`HH:MM` in Australia/Brisbane for daily and weekly), and `keep_last` (a positive count). Hourly backups run at the top of the hour and ignore `time`. Weekly schedules run on Monday by default; an optional lowercase `day` selects another weekday. Each redeployment replaces the schedule; omitting it disables future backups for that database. The scheduler makes one backup when a scheduled time was missed while the API was stopped, then resumes the regular schedule. Existing databases are left unchanged. New database user passwords are generated and stored encrypted in SQLite. An existing PostgreSQL user without a stored credential causes the job to fail rather than altering that user. `GET /databases` lists database records without passwords. `GET /databases/{id}` returns a database’s details, including the decrypted user password and PostgreSQL connection URI. `POST /databases/{id}/backups` starts an immediate backup and returns HTTP 202 with its backup and job IDs. Only databases successfully created and recorded by the API are listed; an existing database left unchanged is not imported. The sample request and document are in [src/http](../http/).

Build and run locally (Go 1.24 or later):

```sh
cd src/api
go test ./...
DB_ENCRYPTION_KEY=<64-hex-character-key> NODES_CONFIG=/path/to/nodes.yaml DATABASE_PATH=/path/to/controller.sqlite go run .
```

Build the container image from the repository root:

```sh
docker build -t ghcr.io/janssen-host/lab-controller:latest src/api
```

The API host Compose configuration expects `nodes.yaml` in `~controller/stacks/api`. Provide that file separately; it is not installed by the commissioning script. The published GHCR image must be built and pushed before the API host can pull it.

To send the example files with curl from the repository root:

```sh
curl -X POST http://10.10.10.2:8080/deploy \
  -F 'forearm=@src/http/test.forearm;type=application/json' \
  -F 'compose=@src/http/docker-compose.yml;type=application/yaml' \
  -F 'env=@src/http/env.json;type=application/json'
```

## Stream container logs

Connect a WebSocket client to `/stacks/{id}/logs?container=<name-or-id>`. The API verifies that the container belongs to the stack, sends up to 500 retained log lines, and then follows new output. Messages contain log text; closing the WebSocket ends the SSH log stream. Use `/stacks/{id}` to find container names and IDs.

For example, with `websocat`:

```sh
websocat 'ws://10.10.10.2:8080/stacks/<stack-id>/logs?container=<container-name>'
```

## Database backup storage

The API runs PostgreSQL `pg_dump` over SSH and streams custom-format dumps directly to `/app/backups`. The API Compose volume mounts the `DB_Backups` NFS export from `10.10.1.7` using NFS version 3 at `/app/backups`. The Docker host must be allowed by the NFS export and the API container must have permission to write it.

Backups and their job statuses appear in `GET /databases/{id}` and `/jobs`. The `path` is relative to the NFS volume root. After each successful backup the scheduler deletes older successful backups beyond `keep_last`; failed runs do not delete good backups. Dumps contain database data, schema, ownership, and privileges, but PostgreSQL global roles are not included and must be available for restoration. Backup dumps are not encrypted by the API.

`POST /stacks/{id}/containers/{container}/restart` stops and starts the named container after verifying it belongs to that stack. It returns 204 when Docker completes the restart.

To update a recorded stack, send `POST /stacks/{id}/update`. The controller pulls configured images, runs Compose up with readiness checks and removes orphan containers, then prunes dangling images on the host. Volumes are retained. Updates use the existing Compose configuration and cannot refresh immutable image tags. A failed update can leave the stack partially updated.

`POST /databases/{id}/backups/{backup}/restore` starts a restore job for a completed backup belonging to that database. Check the returned `job_id` through `GET /jobs/{id}`. The restore disconnects clients, replaces the database using its existing name and user, and restores the dump. A failed restore can leave a partial database.
