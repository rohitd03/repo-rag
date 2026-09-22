# Infrastructure

Two modules with deliberately different status. The difference is stated up
front because it is the only honest way to present them.

| | Status | What it does |
|---|---|---|
| [`local/`](local) | **Applied and in daily use** | Postgres + pgvector in Docker, on port 5433 |
| [`aws/`](aws) | **Validated, never applied** | RDS Postgres with pgvector, the production shape |

`local/` replaced the project's `docker-compose.yml`. Compose describes a
desired state but keeps no record of what it created, so it cannot tell you
what changed and cannot clean up reliably. Terraform keeps state, which is
what makes `plan` meaningful and `destroy` complete.

`aws/` has never been applied — there is no AWS account attached to this
project. It is a design document written in HCL, and it is labelled that way
rather than left for someone to assume otherwise.

---

## Local

```bash
cd terraform/local
terraform init
terraform plan
terraform apply

export RAG_DATABASE_URL=$(terraform output -raw database_url)
```

Or from the project root: `make up`, `make down`, `make plan`.

Destroy removes the container, the volume and the image:

```bash
terraform destroy
```

Data loss on destroy is intentional. The index is derived data — re-indexing
takes about six seconds — and a `destroy` that quietly leaves a 400 MB image
and a stale volume behind is a teardown you cannot trust. Set
`keep_data_on_destroy = true` if you disagree.

### What it manages

```
docker_image.postgres      pgvector/pgvector:pg16
docker_volume.pgdata       rag-postgres-data
docker_container.postgres  rag-postgres, :5433 -> 5432
```

Port 5433 is deliberate, so this coexists with the `cdc-pipeline` database on
5432.

---

## AWS

```bash
cd terraform/aws
terraform init -backend=false
terraform validate
```

`plan` and `apply` need credentials and would create billable resources.

### Decisions worth defending

**pgvector on RDS needs two things, not one.** Postgres 15+ on RDS ships the
extension, but it must be allow-listed via a parameter group *and* created
with `CREATE EXTENSION vector` against the database. Missing the second step
is the usual reason people conclude pgvector "doesn't work on RDS". The
`bootstrap_sql` output exists to make that step hard to forget.

**The password is generated, never passed in.** A password supplied as a
variable ends up in state, in shell history, and frequently in version
control. `random_password` generates it, Secrets Manager stores it, and the
application reads it from there rather than from a Terraform output.

**Ingress is by security group, not CIDR.** Access is granted to the
application's identity rather than to a range of IP addresses. A CIDR rule is
the quicker version and is also the thing a reviewer stops on.

**Backups are short and Multi-AZ is off by default.** The index is derived
data — the source of truth is the git repository, and rebuilding takes
seconds. Paying roughly double for Multi-AZ to protect a cache is the kind of
default that quietly costs money for years. Both are variables, so an
environment that disagrees can say so explicitly.

**`skip_final_snapshot` is tied to `deletion_protection`.** Otherwise
`destroy` fails on a protected instance and the teardown path stays untested
until the day it is urgent.

### What is missing before this could be applied

- A remote state backend (S3 + DynamoDB lock). Local state is fine for a
  single operator and wrong for a team.
- Compute. This provisions the database only; the indexer and API would need
  ECS or Lambda definitions.
- A CI plan/apply pipeline with a human gate on apply.
- Real cost estimates. `db.t4g.micro` is a placeholder, not a sizing exercise.

Listing these is the point. An infrastructure module that claims to be
complete and is not is worse than one that says where it stops.
