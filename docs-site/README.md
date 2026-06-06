# Mnemos docs site

Source for the technical reference docs at [docs.mnemos.dev](https://docs.mnemos.dev/).

The marketing landing page (https://klarlabs-studio.github.io/mnemos/) lives in
`/docs/index.html` on `main` — different content, different audience, different
build pipeline. This directory is the mkdocs-material site for the
SDK / API / benchmark reference.

## Build locally

```bash
pip install mkdocs-material
mkdocs serve
# http://127.0.0.1:8000
```

## Build static

```bash
mkdocs build --strict --site-dir _site
```

## Deploy

The CI workflow at `.github/workflows/docs.yml` builds the site on every
push to `main` that touches `docs-site/**` and uploads it as an artifact.
Wiring it to a publishing target (gh-pages branch on a separate repo,
Read the Docs, Cloudflare Pages, etc.) is intentionally left as the next
step — pick the host that matches your infra.
