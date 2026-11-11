# Renovate

`.woodpecker/renovate.yaml` runs Renovate against this repository. It runs on a
Woodpecker cron named `renovate` or a manual trigger and opens update pull
requests; it does not auto-merge.

## One-time setup

1. Create a Forgejo bot account and a personal access token. Renovate needs
   `repo` read/write, `user` read, and `issue` read/write.
2. Add the token to this Woodpecker repository as the secret `renovate_token`.
3. Create a read-only GitHub token for public repository metadata and add it as
   the secret `renovate_github_com_token`; Renovate uses it for GitHub release
   lookups and changelogs.
4. In Woodpecker repository settings, add a cron named `renovate` on the `main`
   branch with schedule `@daily`.

The workflow targets the current repository (`CI_REPO`) on the Forgejo API and
pulls the Renovate image on every run, so the workflow itself contains the only
Renovate version to maintain.

## Version coverage

The built-in managers cover every pinned version in this repository; no custom
regex matcher is needed:

| File | Manager | Versions |
| --- | --- | --- |
| `.woodpecker/ci.yaml`, `.woodpecker/renovate.yaml` | `woodpecker` | step images |
| `Dockerfile` | `dockerfile` | base images and `apk` packages |
| `docker-compose.yaml` | `docker-compose` | Mosquitto image |
| `go.mod` | `gomod` | modules and the `go` directive |
| `e2e/package.json` | `npm` | `@playwright/test` |

Image references must stay literal `image:` values, because the Woodpecker
manager reads them from the YAML. Do not move them into YAML anchors or
`variables:` entries.

`renovate.json` groups `mcr.microsoft.com/playwright` with `@playwright/test`:
the E2E image provides the browsers for the npm test runner and both must stay
on the same release. All other updates arrive as separate pull requests.

The CI pipeline lives in `.woodpecker/ci.yaml` rather than `.woodpecker.yml`
because Woodpecker prefers the `.woodpecker/` directory over the single file
when both exist; `.woodpecker/renovate.yaml` makes the directory necessary.
