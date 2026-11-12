# Contributing

## Toolchain

| Tool | Requirement |
| --- | --- |
| Go | >= 1.27.0; selected toolchain 1.27.1 |
| Terraform | >= 1.11.0 and < 2.0.0 |
| Task | v3 |
| Python | >= 3.9 |
| Shell | Bash and standard Unix tools |
| Git | Required for repository publication checks |
| C compiler | Required for the Go race detector |

The harness sets `GOTOOLCHAIN=local`. Install the selected Go toolchain before
running tasks. Go dependencies are pinned in `go.mod`/`go.sum`; documentation-tool
dependencies are isolated in `tools/`.

## Setup and commands

```sh
task setup
task verify
```

`task setup` checks installed tools and downloads pinned dependencies using the
configured Go proxy. Caches and local test settings are stored in `.harness/`;
provider binaries are written to `bin/`. These directories are ignored by Git.

| Command | Operation |
| --- | --- |
| `task doctor` | Check installed tools |
| `task fmt` | Format Go and Terraform examples |
| `task check` | Check formatting, dependencies and static analysis |
| `task test` | Run unit tests |
| `task test PKG=./internal/client TEST=TestDo` | Run selected package tests |
| `task test:race` | Run the race detector |
| `task test:cover` | Write `.harness/coverage.out` and print coverage |
| `task build` | Build the provider |
| `task build VERSION=0.0.1` | Set the build version |
| `task dev` | Build and write a local Terraform development override |
| `task smoke` | Load the plugin schema and validate the provider example |
| `task docs` | Generate and validate Registry documentation |
| `task docs:check` | Validate documentation and check generated-file drift |
| `task test:harness` | Test documentation and repository tooling |
| `task ci:lint` | Validate GitHub Actions workflows with pinned actionlint |
| `task repository:check` | Validate the Git publication file set |
| `task test:acc` | Run explicitly configured local API acceptance tests |
| `task verify` | Run the complete local verification gate |
| `task deps:tidy` | Reconcile both Go modules after dependency changes |
| `task release:check` | Validate the GoReleaser configuration |
| `task release:snapshot` | Build unsigned release archives without publishing |
| `task clean` | Remove generated binaries and local harness data |

Unit tests, schema validation and documentation generation do not require
OnPager credentials or a running backend. Test implementations use HTTP fixtures
and the installed Terraform CLI; no Terraform binary is downloaded by the harness.

## Repository structure

| Path | Contents |
| --- | --- |
| `internal/client/` | HTTP transport, API DTOs and client tests |
| `internal/provider/` | Provider, resource and data-source schemas, state conversion and tests |
| `templates/` | Registry documentation sources |
| `docs/` | Generated provider, resource, data-source and guide pages |
| `examples/` | Standalone Terraform configurations |
| `scripts/harness/` | Development and validation tooling |
| `tests/harness/` | Tooling tests |
| `tools/` | Pinned documentation generator module |
| `.github/` | Shared CI toolchain setup and verification/release workflows |

## Continuous integration

[CI](.github/workflows/ci.yml) runs on pull requests and pushes to `main` or
`master`, and supports manual runs. A workflow lint job runs actionlint
`v1.7.12`. The verification jobs run `task verify` with Terraform
`1.11.0` and `1.17.1`: publication boundaries, formatting, both Go modules,
static analysis, race tests, plugin schema loading, documentation drift and
standalone example validation. A separate job validates the GoReleaser
configuration and builds all release archives without signing or publishing.

The shared setup installs the Go toolchain selected in `go.mod`, Task `3.53.1`
and the selected Terraform CLI. Go caches use `.harness/` and include both module
sum files in the cache key. Actions are pinned to commit SHAs; GoReleaser is pinned
to `v2.19.0`. Verification jobs use read-only repository permissions and require
no secrets. Live API acceptance tests remain explicitly configured local tests.

[Dependabot](.github/dependabot.yml) checks GitHub Actions and both Go modules
weekly. Updates remain pull requests reviewed through the same CI gate; no
automatic merge or release is configured. Tool versions in workflow inputs and
Task commands, including Terraform, Task, GoReleaser and actionlint, need explicit
maintenance because Dependabot does not manage these arbitrary version strings.

## Releases

[The release workflow](.github/workflows/release.yml) runs for pushed `v*` tags.
Use semantic versions, initially `v0.0.1`; prerelease tags such as `v0.0.2-rc.1`
are also supported. A read-only preflight checks that the tag is a valid
v-prefixed Semantic Version, resolves to the event's commit, matches the checkout
and points to a commit in the repository's current default branch. A branch must
not have the same name as the tag. Unavailable references, unprovable ancestry
or missing event metadata fail the check. Preflight performs no network requests
or Git mutations.

The complete reusable CI workflow must pass before the signing job, which alone
receives `contents: write`. The signing job uses the `release` environment and
repeats preflight on its own checkout before importing the key. GoReleaser uses
the validated event tag explicitly, including when RC and stable tags share a
commit. These checks complement the GitHub rules below; they do not replace
restrictions on who can create tags or approve access to secrets.

### GitHub repository settings

Configure these settings before adding a private signing key:

1. Allow the SHA-pinned actions used by these workflows. Keep default workflow
   token permissions read-only; the signing job requests its own write access.
   Keep write tokens and secrets unavailable to fork pull requests.
2. Protect the default branch with a pull-request requirement, required status
   checks, and restrictions on force pushes and deletion. Select the checks
   after the first CI run: `Workflow lint`, `Verify / Terraform 1.11.0`,
   `Verify / Terraform 1.17.1` and `Release snapshot`. GitHub may display these
   under a workflow prefix. Require branches to be up to date before merging.
   Require review when another eligible maintainer is available.
3. Add two tag rulesets targeting `v*`. One restricts tag creation, with bypass
   limited to release maintainers. The other restricts updates and deletion,
   without routine bypass access. Keeping these rules separate prevents the
   permission to create a tag from also allowing released tags to be rewritten.
4. Create an environment named `release`. Set deployment branches and tags to
   selected **tags** matching `v*`; do not select protected branches only for
   this tag-triggered job. Add required reviewers and disable administrator
   bypass. Enable prevention of self-review when another release approver is
   available. Review the tagged source and workflow before approving the job.
5. Store the following credentials and variable in that environment. Remove
   duplicate repository-level or inherited organization-level signing secrets;
   those broader copies would remain available outside the environment gate.

| Environment setting | Value |
| --- | --- |
| Secret `GPG_PRIVATE_KEY` | ASCII-armored private release-signing key |
| Secret `PASSPHRASE` | Nonempty passphrase for that key |
| Variable `GPG_FINGERPRINT` | Full 40-character RSA primary-key fingerprint, without spaces |

The imported primary-key fingerprint must match `GPG_FINGERPRINT` before
GoReleaser runs. Obtain it with `gpg --fingerprint`; it must belong to the public
key registered for the namespace. The private key and passphrase must not be
committed, printed in workflow logs or stored in public artifacts.

Add a `.github/CODEOWNERS` file using actual maintainer accounts or teams for
`.github/`, `.goreleaser.yml`, `Taskfile.yml` and `scripts/harness/release-preflight.py`,
then require code-owner review when those owners are available. Enable
Dependabot alerts and secret scanning with push protection where available.
Choose and add a repository `LICENSE` before public distribution.

### Release and developer signing keys

Use a dedicated RSA 4096-bit GPG key for release artifacts, protected with a
nonempty passphrase. Keep an encrypted backup and revocation certificate outside
the repository, record ownership and expiry, and renew or rotate it before it
expires. Register a replacement public key before switching CI to it. Keep old
public verification keys available for published versions. The Registry's
documented signing-key contract accepts RSA/DSA, not default ECC keys.

Developer commits and release tags should use individual signing keys. SSH
signing is supported by Git and GitHub and keeps personal private keys out of
CI. Add the public key to GitHub as a signing key and configure local Git, using
the path of the chosen key:

```sh
git config --local gpg.format ssh
git config --local user.signingkey "$HOME/.ssh/id_ed25519.pub"
git config --local commit.gpgsign true
git config --local tag.gpgsign true
```

The corresponding private key must be available locally, for example through
`ssh-agent`. After maintainers can produce verified commits, enable required
signed commits on the default branch. Do not rewrite existing history solely
to add signatures. Release preflight checks tag identity and ancestry; it does
not verify a tag signature against a trusted developer-key list. Required signed
commits also do not enforce signed tags. Review tag signatures separately until
an explicit trusted-key policy is configured.

See [GitHub signature verification](https://docs.github.com/en/authentication/managing-commit-signature-verification/about-commit-signature-verification)
and [environment protection](https://docs.github.com/en/actions/how-tos/deploy/configure-and-manage-deployments/manage-environments).

### Public Registry registration

The repository must be public. For an HCP-managed namespace, install the
Terraform Cloud GitHub App with access to this repository and create or claim
the `onpager` namespace in the HCP Terraform organization. An HCP organization
or project alone does not establish public distribution.

In **HCP Terraform > Registry > Public namespaces > onpager > Settings**, add
the ASCII-armored public GPG key. After claiming the namespace, manage its keys
and providers through HCP Terraform. Once the first finalized GitHub release
exists, open the namespace, select **Publish > Provider**, choose the repository
and category, and complete provider registration. Later finalized GitHub releases
are ingested through the GitHub integration. These are public-registry actions;
adding a provider to a private registry is a separate workflow.

See HashiCorp's [provider release requirements](https://developer.hashicorp.com/terraform/registry/providers/publishing),
[namespace key management](https://developer.hashicorp.com/terraform/cloud-docs/users-teams-organizations/organizations/public-namespace/manage)
and [public provider registration](https://developer.hashicorp.com/terraform/cloud-docs/users-teams-organizations/organizations/public-namespace/artifacts).
The workflow uses GitHub's automatic `GITHUB_TOKEN`; it does not need a separate
personal access token or OnPager credentials.

### Preparing a version

After merging and checking out the release commit from the default branch,
create a signed annotated tag with the maintainer's configured signing key:

```sh
git tag -s v0.0.1 -m "Release v0.0.1"
git push origin v0.0.1
```

GoReleaser builds static provider binaries for Linux and macOS (`amd64`, `arm64`)
and Windows (`amd64`), embedding the tag version in `main.version`. It creates a
**draft** GitHub release with five ZIP archives, the renamed Registry manifest,
SHA-256 checksums covering the archives and manifest, and a binary detached GPG
signature of the checksums. Review the draft and publish it in GitHub Releases.
The job also verifies the generated checksum signature locally. Drafts are not
ingested by the Registry. Before publication, verify the downloaded signature
against the registered key, check the archive checksums and confirm the version
and manifest. Verify each version appears in the public Registry and installs
from a clean Terraform configuration without development overrides or a local
provider mirror. Release versions must not be overwritten; publish fixes under
a new version.

For local packaging checks, install GoReleaser `v2.19.0`, then run:

```sh
task release:check
task release:snapshot
```

Snapshot output is written to ignored `dist/` with a
`0.0.0-dev+<commit>` version. Signing keys and GitHub credentials are not required.
The manifest's numeric `version: 1` describes its format; protocol version stays
`6.0`. Neither value is the provider release version. Ordinary development builds
continue to report `dev` unless `VERSION` is supplied.

## Local provider installation

```sh
task dev
export TF_CLI_CONFIG_FILE="$PWD/.harness/terraform.tfrc"
terraform -chdir=examples/provider providers schema -json
terraform -chdir=examples/provider validate
```

The override maps `onpager/onpager` to the local `bin/` directory. It does not
change global Terraform settings. Schema loading and validation use the override
directly; `terraform init` is not required for these development checks. Regenerate
the override after moving the checkout.

Configure API credentials through provider arguments or `ONPAGER_ENDPOINT` and
`ONPAGER_TOKEN` before running resource operations. Provider configuration performs
no API requests. HTTP requests use context cancellation, a 30-second timeout and
TLS certificate verification. Mutation retries are not automatic.

## Resource and data-source changes

1. Verify the API operations, DTOs, authorization, concurrency and retirement
   contracts before defining a Terraform schema.
2. Add typed client methods in `internal/client/` and schema/state conversion in
   `internal/provider/`. Register the constructor in the provider.
3. Test null/empty/default semantics, stable plans, import, drift, malformed
   responses, committed partial writes and error-state retention. Resource reads
   and data-source reads must not mutate configuration.
4. Add the documentation template and standalone examples, then run `task docs`
   and `task verify`.

Keep API DTOs independent from Terraform models. Use exact detail reads and
stable API identities. Preserve each operation's concurrency contract; do not
replace a stale expected revision automatically. Dataset/enrichment aggregate
ownership and historical retirement behavior must remain explicit.

## Documentation sources

Edit source files, then regenerate documentation; do not edit `docs/` directly.

| Source | Purpose |
| --- | --- |
| Schema descriptions in `internal/provider/` | Arguments, attributes, types and constraints |
| `templates/index.md.tmpl` | Provider overview |
| `templates/guides/` | Configuration, lifecycle and terminology guides |
| `templates/resources/<name>.md.tmpl` | Resource lifecycle, permissions and import identity |
| `templates/data-sources/<name>.md.tmpl` | Selectors, read behavior and permissions |
| `examples/resources/onpager_<name>/` | `resource.tf`, `terraform.tf` and import examples |
| `examples/data-sources/onpager_<name>/` | `data-source.tf` and `terraform.tf` |

`tfplugindocs` is pinned in `tools/go.mod`. The harness exports the real plugin
schema, generates pages in a temporary directory, validates Registry structure
and local links, and runs `terraform validate` for standalone examples before
publication. `task docs:check` compares a fresh generation with committed pages.

Use technical English and the [glossary](docs/guides/glossary.md). Document actual
scope, defaults, null/empty behavior, replacement, state persistence, required
permissions and destroy behavior. Generated schema tables are the field reference;
do not duplicate them in manually maintained tables.

The composed [team configuration](examples/team-configuration) follows the
standard Terraform file structure. The [Vault example](examples/provider-vault)
requires the external Vault provider and an accessible Vault service; routine
validation checks its formatting but does not initialize or execute it.

## Acceptance tests

Run against an explicitly configured disposable local OnPager installation:

```sh
export ONPAGER_ACC_ENDPOINT="http://127.0.0.1:8080"
export ONPAGER_ACC_TOKEN="<Control bearer token>"
export ONPAGER_ACC_USER_ID="<active user UUID>"
task test:acc
```

Alternatively, store `endpoint`, `token` and `user_id` in ignored
`.harness/live-test.json` with owner-only file permissions. Environment variables
take precedence. The token requires the native grants for all tested operations,
including separate sensitive dataset-row reads. Token ownership grants no
business permissions.

Tests create unique `tfacc-*` definitions. Cleanup uses the supported API deletion,
revocation, disable or archive operations. Existing profiles, shared templates and
unowned configuration are not test fixtures. No alert admission, notification
send, preview or direct database operation is part of these tests.

Additional fixtures and opt-in coverage:

- Bot and voice lifecycle tests create fresh disabled connections with synthetic
  credentials. Control must have the corresponding credential keyrings. All five
  messengers and Telegram/Pachca webhook modes are tested without requiring
  `BOT_CALLBACK_ORIGIN`. Negative callback-precondition checks require a stand
  without that setting, observed through the exact test-owned bot; they are
  explicitly skipped when an origin is configured. They verify POST's default
  enabled state, PUT's required flag and atomic rejection of enabling, without
  accepting an enabled synthetic fixture or changing deployment settings.
- Catalog bindings require `ONPAGER_ACC_DELIVERY_CHANNEL_ID`,
  `ONPAGER_ACC_DELIVERY_TARGET_ID` and `ONPAGER_ACC_DELIVERY_TARGET_ALT_ID`.
  The channel must be enabled, globally available and conversation-capable;
  targets must be distinct and belong to that channel. Populated handover-channel
  tests use the same fixtures. Tests do not discover targets.
- Data-source user selectors can use `ONPAGER_ACC_DS_USER_ID`,
  `ONPAGER_ACC_DS_LOCAL_LOGIN` and `ONPAGER_ACC_DS_EMAIL`; all must identify the
  same existing user. Without the first, the user UUID falls back to
  `ONPAGER_ACC_USER_ID`. `ONPAGER_ACC_RBAC_BUILTIN_KEY` enables builtin-role reads.
- Aggregate enrichment rules require
  `ONPAGER_ACC_ENRICHMENT_RULES_DISPOSABLE=1` and an initially empty tenant
  snapshot. Tests guard ownership and use CAS. Run credential variants
  sequentially because the snapshot is shared.
- Team-role-defaults mutation tests require
  `ONPAGER_ACC_TEAM_ROLE_DEFAULTS_DISPOSABLE=1` and initially empty member/lead
  sets. The test owns the complete tenant policy temporarily, uses its own CAS
  revision and clears only that disposable policy on cleanup. Existing seeded
  or UI-managed defaults must not be changed or restored by routine acceptance.
  Without the fixture, this live mutation case is explicitly skipped.
- Global notification-template CRUD has no isolated owner and remains covered by
  local fixtures. Missing optional fixtures produce explicit coverage exclusions.

## Git publication

Run `task repository:check` before staging files. Local agent settings, internal
notes, credentials, Terraform state/plans, caches, binaries and editor metadata
are excluded. The check also detects prohibited or ignored files already present
in a Git index. Keep development evidence and operational records outside the
public file set.
