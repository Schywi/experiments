# site — the published documentation

Quartz 4 renders `research/`, `evolution/`, `benchmark/` and `blog/` as a static
site with an Obsidian-style graph. Those four directories at the repository root
remain the source of truth; this directory holds only the renderer and its
configuration.

Published at <https://schywi.github.io/experiments/>.

## Build locally

```bash
cd site
npm ci
npm run serve        # assembles content/, builds, serves on :8080
```

`npm run serve` and `npm run build` both run `scripts/site-sync.sh` first, which
mirrors the four documentation trees into `site/content/`. That directory is
gitignored: it is a build artefact, and editing it is pointless because the next
sync overwrites it. Edit the notes where they live.

## What is configured

| File | Holds |
|---|---|
| `quartz.config.ts` | Site title, `baseUrl` (`schywi.github.io/experiments`), theme, plugin list |
| `quartz.layout.ts` | Sidebar explorer, search, the graph (local depth 2, global on hover), backlinks |
| `landing.md` | The site's home page, copied to `content/index.md` by the sync script |

The graph is Quartz's own d3 graph: the right-hand pane shows the local
neighbourhood of the note you are reading, the icon in its corner expands to the
whole vault, and clicking a node navigates to that note.

## Deployment

`.github/workflows/pages.yaml` builds on every push to `main` and deploys with
`actions/deploy-pages`. The repository setting is **Pages → Source: GitHub
Actions**; nothing is committed to a `gh-pages` branch.

## Attribution

Quartz is MIT-licensed; `LICENSE.txt` in this directory is its licence.
