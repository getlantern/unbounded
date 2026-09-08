# Unbounded WordPress Plugin

The WordPress front end for the Unbounded widget. It prints the widget element,
loads the widget bundle, and adds a settings screen and a per-page toggle.
Everything else — peer discovery, transport, the WebAssembly proxy — happens in
the service.

| File | Role |
|---|---|
| `unbounded.php` | The plugin |
| `readme.txt` | The wordpress.org readme (this file is dev docs; that one ships) |
| `uninstall.php` | Removes the option and post meta on delete |
| `docker-compose.yml` | WordPress + MySQL for local testing |
| `demo-site/` | The Playground demo, and the build that produces the plugin zip |

## Local testing with Docker

```bash
docker compose up -d
mkdir -p ./wp-content/plugins/unbounded
cp unbounded.php readme.txt uninstall.php ./wp-content/plugins/unbounded/
```

Then:

- Activate at `http://localhost:8000/wp-admin/plugins.php`
- Configure at `http://localhost:8000/wp-admin/admin.php?page=browsers-unbounded-settings`
- Tick **Display on Homepage**, or edit a page and tick **Enable Unbounded on
  this page** in the editor sidebar
- View the page; the widget renders with its switch off

Port 8000 collides often. `docker compose -f docker-compose.yml -f override.yml up -d`
with a `ports: !override ["8001:80"]` block moves it.

## Building the plugin zip

```bash
bash demo-site/build.sh
```

Produces `demo-site/dist/unbounded.zip` alongside the demo site. The archive's
top-level directory is `unbounded/`, which is the plugin's permanent slug on
wordpress.org — do not rename it. `build.sh` fails if `unbounded.php`'s
`Version` and `readme.txt`'s `Stable tag` disagree, since wordpress.org serves
whatever `Stable tag` points at.

The same zip serves the demo and the directory submission, so the two cannot
drift apart.

## Naming

The `<browsers-unbounded>` element name and the `browsers_unbounded_*`
function/option prefixes predate the rename from Browsers Unbounded. The element
name is the widget bundle's public API — `ui/src/index.tsx` matches only
`browsers-unbounded` or the legacy `lantern-network` — so renaming it would
render nothing. The prefixes stayed with it for consistency.

## Submitting to the WordPress Plugin Directory

See `readme.txt`'s **External services** section first: the plugin loads its
bundle from `embed.lantern.io`, which needs the Software-as-a-Service reading of
[guideline 8](https://developer.wordpress.org/plugins/wordpress-org/detailed-plugin-guidelines/#8-plugins-may-not-send-executable-code-via-third-party-systems).
That section is the argument, so keep it accurate.

Submit `unbounded.zip` at <https://wordpress.org/plugins/developers/add/> from a
`@getlantern.org` account, with `plugins@wordpress.org` whitelisted. Review
takes up to 14 business days; approval grants an SVN repo, and the plugin goes
live once pushed there.
