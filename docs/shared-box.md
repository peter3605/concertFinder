# concertFinder on the shared apps box

**Status: live since the cutover (step 26).** concertfinder.app is served by
the shared box in the apps account (`426433020884`), deployed by
`.github/workflows/image.yml` on every merge to `main`. `deploy.yml` only runs
the checks now; its old-box deploy job was removed at the cutover. The old box
in the management account stays up, api stopped, as the rollback until step 27
(apps-infra `cutover/cutover.sh rollback`). Built by
[apps-infra](https://github.com/peter3605/apps-infra).

## What runs where

| | Old box (today) | Shared box (after cutover) |
|---|---|---|
| Compose file | `docker-compose.prod.yml` | `docker-compose.box.yml` (project `concertfinder-box`) |
| Image | built on the instance | pulled from ECR `concertfinder-api:<sha>` (CF-B20 builds it) |
| TLS | this repo's `Caddyfile`, own Caddy container | apps-infra `caddy/Caddyfile`, one shared Caddy (project `shared`) |
| Api reachable as | `api:8080` on the project network | `concertfinder-api:8080` on the external `edge` network |
| Deploy entry point | `AWS-RunShellScript` from deploy.yml | `concertfinder-deploy` SSM document → `/opt/concertfinder/deploy.sh` |
| Secrets | SSM `/concertfinder/*`, management account | SSM `/concertfinder/*`, apps account (same names) |
| Database | Neon | RDS `shared-postgres` |

`container_name: concertfinder-api` in `docker-compose.box.yml` and
`reverse_proxy concertfinder-api:8080` in apps-infra's Caddyfile are the two
halves of one contract in two repos. `check-deploy-config.sh` pins this half;
change neither alone.

## One-time setup on the box (step 24, after this repo's PR has merged)

`/opt/concertfinder` (created by apps-infra's `user_data` as the
`concertfinder` user's home) becomes a git checkout of this public repo.
**Do this only once `deploy.sh` is on `main`**: a checkout of an older `main`
has no `deploy.sh`, and the SSM document fails at its `test -x`.

Over Session Manager, as root:

```sh
cd /opt/concertfinder
runuser -u concertfinder -- git init -q
runuser -u concertfinder -- git remote add origin https://github.com/peter3605/concertFinder.git
runuser -u concertfinder -- git fetch -q origin main
runuser -u concertfinder -- git checkout -q --detach FETCH_HEAD
test -x deploy.sh && echo ready
```

`git` itself comes from apps-infra's `user_data`. The existing `.docker/`
(the ECR credential helper's config) is untracked and gitignored, so the
checkout leaves it alone.

SSM `/concertfinder/*` in the apps account must hold every variable the old
box's does, with `DATABASE_URL` pointing at RDS (`?sslmode=require`) and
`SITE_DOMAIN=concertfinder.app`. `render-env.sh` refuses to render while any
parameter still reads `REPLACE_ME`.

## Deploy

The deploy workflow (CF-B20) pushes `<ecr>/concertfinder-api:<sha>` and runs:

```sh
aws ssm send-command --document-name concertfinder-deploy \
  --instance-ids <box_instance_id> --parameters ImageTag=<sha> \
  --comment "Deploy from GitHub Actions run <id>"
```

The document runs `/opt/concertfinder/deploy.sh` (→ `scripts/deploy-box.sh`)
as `concertfinder` with `IMAGE` set. That script:

1. checks out `<sha>`, then re-runs itself from that commit, so the compose
   file and scripts always match the image;
2. renders `.env` from SSM with `scripts/render-env.sh`, unchanged;
3. appends `CONCERTFINDER_IMAGE` to `.env`, so `docker compose -f
   docker-compose.box.yml ps|logs` keeps working between deploys;
4. pulls, then `up -d --wait`;
5. has the shared Caddy container fetch `http://concertfinder-api:8080/api/healthz`
   over `edge`: the exact hop Caddy proxies;
6. runs `scripts/verify-deploy.sh`: HTTPS through Caddy, then the database-host
   check (CF-B19).

One deploy at a time: `flock` on `/opt/concertfinder/.deploy.lock`.

**Before the cutover** the A record still points at the old box, so the shared
Caddy cannot get a certificate for concertfinder.app and no TLS probe can
pass. Step 6 therefore skips **only** the HTTPS probe, loudly, while the name
does not resolve to this box's public IP; step 5 and the database check still
run. After the A record moves, the HTTPS probe is mandatory again. A warning
after the cutover means the A record is wrong.

## Rollback

Run the same document with the previous SHA. ECR keeps the last three images,
and the checkout moves back with the image. That only reaches commits that
contain `deploy.sh`, which is every commit from this one on.

## Which scripts stay per app

| Script | Where it lives on the box | Why |
|---|---|---|
| `render-env.sh` | per app, called by `deploy-box.sh` | Reads this app's SSM path into this app's `.env`. |
| `verify-deploy.sh` | per app, called by `deploy-box.sh` | Knows this app's health route and its `DATABASE_URL`. |
| `restore-drill.sh` | per app, operator tool | Restores this app's schema and asserts its tables. It targets Neon and the old bucket layout, and needs retargeting to RDS and `weekly/concertfinder/` before it means anything on the box. |
| `backup-db.sh` | **apps-infra, box-level** | The database is now one RDS instance for every app, and the bucket's `weekly/<app>/` layout (apps-infra `backups.tf`) was drawn for a single box job that dumps each app's database. Per-app copies would each need their own `PG_IMAGE` pin, timer and S3 key scheme for what is one operation. This script stays for the old box until cutover. |
| `prune-images.sh` | **apps-infra, box-level** | Disk and the image store are shared by every app; one app's prune cannot reason about the others'. No build happens on the box, so the buildkit half is moot. Rollback targets live in ECR, not in the local cache. Stays for the old box until cutover. |
| `watchdog.sh` | **apps-infra, box-level** | The failure it was built for, a Caddy crash loop beside a healthy api, now happens in the `shared` project, which no app owns. A per-app watchdog would see only `concertfinder-api` and miss exactly that. The logic is already generic (`COMPOSE_FILE`, `COMPOSE_PROJECT_NAME`, `WATCHDOG_NAMESPACE`); the box job iterates every project. Stays for the old box until cutover. |

Until those box-level jobs exist in apps-infra, the shared box has no
container watchdog and no image prune. RDS's own 14-day automated backups
cover the database.
