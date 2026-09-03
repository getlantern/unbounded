# Unbounded WordPress plugin — demo site

A one-click live demo of the plugin. No server runs WordPress: the landing page
links to [WordPress Playground](https://playground.wordpress.net), which boots
WordPress compiled to WebAssembly inside the visitor's browser, installs
`wp-plugin.zip`, activates it, and lands on a page with the widget embedded.

## Layout

| File | Role |
|---|---|
| `index.html` | Landing page. Builds the Playground link at runtime from `location.origin`, so it works on any domain with no edits. |
| `blueprint.json` | Playground setup steps. `PLUGIN_ZIP_URL` is substituted by `index.html`. |
| `_headers` | Permissive CORS. Belt-and-braces: Playground fetches plugin zips through its own proxy, so this is not currently load-bearing. |
| `build.sh` | Assembles `dist/`, generating `wp-plugin.zip` from the plugin source. |

`dist/` is generated and gitignored.

## Cloudflare Pages settings

The project is connected to this repo, so a push to `main` publishes and every PR
gets a preview URL.

```
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

`build.sh` then serve `dist/` over **HTTPS** — Playground is an HTTPS origin, and a
plugin zip on `http://localhost` is blocked as mixed content, which surfaces only as
a Playground that hangs on "Loading Playgrounds". A preview deployment is usually the
faster way to check a change.

For testing the plugin against real WordPress and MySQL instead, see the
`docker-compose.yml` one directory up.
