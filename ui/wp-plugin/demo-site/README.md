# Lantern Action Mode WordPress plugin — demo site

A one-click live demo of the plugin. No server runs WordPress: the landing page
links to [WordPress Playground](https://playground.wordpress.net), which boots
WordPress compiled to WebAssembly inside the visitor's browser, installs
`unbounded-by-lantern.zip`, activates it, and lands on a page with the widget embedded.

## Layout

| File | Role |
|---|---|
| `index.html` | Landing page. Builds the Playground link at runtime from `location.origin`, so it works on any domain with no edits. |
| `blueprint.json` | Playground setup steps. `PLUGIN_ZIP_URL` is substituted by `index.html`. |
| `_headers` | Permissive CORS. Belt-and-braces: Playground fetches plugin zips through its own proxy, so this is not currently load-bearing. |
| `build.sh` | Assembles `dist/`, generating `unbounded-by-lantern.zip` from the plugin source. |
| `404.html` | Not-found page. Without it Cloudflare Pages answers every unknown path, including `/robots.txt`, with `index.html` and a 200. |
| `robots.txt`, `sitemap.xml` | Point crawlers at the one page. Bump `lastmod` when the page changes. |
| `favicon.svg`, `og.png` | Icon and the 1200×630 link-preview image, a screenshot of `tools/og.html`. |
| `tools/faq.py` | Writes the FAQ section and the JSON-LD block in `index.html` from one list. |

`dist/` is generated and gitignored.

## The FAQ and structured data

The FAQ lives in `tools/faq.py`, which writes both the visible FAQ section and the
schema.org `FAQPage` markup into `index.html`, so the two always say the same thing.
To change a question or answer, edit the list there and run `python3 tools/faq.py`.
The same script writes the rest of the JSON-LD graph (`Organization`, `WebSite`,
`WebPage`, and a `SoftwareApplication` for the plugin's WordPress Plugin Directory
listing). Every answer must be backed by `../readme.txt`, `../unbounded.php`, these
READMEs or the repository README.

## Cloudflare Pages settings

The project is connected to this repo, so a push to `main` publishes and every PR
gets a preview URL.

```text
Build command:            bash ui/wp-plugin/demo-site/build.sh
Build output directory:   ui/wp-plugin/demo-site/dist
Root directory:           /
```

Optionally set *Build watch paths* to `ui/wp-plugin/*` so unrelated pushes do not
trigger a rebuild.

## Why the zip is built, not committed

The previously published archive lived at `unbounded.s3.amazonaws.com/wp-plugin.zip`
and was uploaded by hand in February 2024. The plugin source kept changing; that
object did not. By 2026 it still shipped the pre-rebrand build whose front-end hook
predated the homepage and posts settings entirely — so a demo created from it
installed a plugin that silently ignored half the options the settings screen
offered. `build.sh` regenerates the zip on every deploy so the demo can only ship
what is actually in the tree.

## Testing locally

Run `build.sh`, then serve `dist/` over **HTTPS** — Playground is an HTTPS origin, and a
plugin zip on `http://localhost` is blocked as mixed content, which surfaces only as
a Playground that hangs on "Loading Playgrounds". A preview deployment is usually the
faster way to check a change.

For testing the plugin against real WordPress and MySQL instead, see the
`docker-compose.yml` one directory up.
