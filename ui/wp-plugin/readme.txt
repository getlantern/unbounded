=== Unbounded ===
Contributors: getlantern
Tags: censorship, privacy, proxy, volunteer, webrtc
Requires at least: 5.0
Tested up to: 7.1
Requires PHP: 7.4
Stable tag: 1.2.0
License: GPL-3.0-or-later
License URI: https://www.gnu.org/licenses/gpl-3.0.html

Let visitors volunteer a slice of their connection to help people in censored regions reach the open internet.

== Description ==

Roughly a third of the world's internet users live behind some form of national
censorship. Unbounded lets the people who visit your site help, without
installing anything: the widget adds a switch to your page, and a visitor who
turns it on lends a small amount of their bandwidth to relay traffic for someone
whose government blocks the open internet.

Nothing happens until a visitor chooses it. The switch is off when the page
loads, and the proxy engine is not even downloaded until it is turned on.
Visitors can turn it off at any time, and closing the tab stops it.

This plugin is the WordPress front end for that widget. It adds a settings
screen for the widget's appearance and placement, and a checkbox in the page
editor to turn it on for a given page.

= What you can configure =

* **Layout** — banner, panel, floating, or simple
* **Theme** — light, dark, or follow the visitor's system preference
* **Location** — top or bottom of the page
* **Where it appears** — per page via the editor sidebar, on all posts, or on the homepage

= Open source =

Unbounded is developed in the open at
[github.com/getlantern/unbounded](https://github.com/getlantern/unbounded).
The plugin source lives in `ui/wp-plugin`, and the widget it loads is in `ui/`.

== External services ==

This plugin is a client for the Unbounded network, run by Lantern. The widget
cannot work locally: relaying traffic for someone in another country requires
peers, signaling, and exit infrastructure, all of which live in the service.

When the widget renders on a page -- before the visitor has done anything --
their browser contacts:

* **embed.lantern.io** -- serves the widget bundle and its images, and hosts a
  small invisible frame the widget uses to remember volunteering statistics.
* **fonts.googleapis.com / fonts.gstatic.com** -- the widget's typeface,
  Urbanist, from Google Fonts.
* **plausible.io** -- usage analytics, loaded inside the embed.lantern.io frame.
  The widget sends it two events: that it loaded, and later whether it was
  switched on. Plausible's own script adds its usual page metadata, including
  the frame's referrer -- the site the widget is embedded on, usually just the
  origin, as trimmed by that site's referrer policy. Plausible is cookieless
  and keeps no persistent identifier for the visitor; Lantern uses the counts
  to report how many sites run the widget and how many visitors volunteer.

Nothing else is contacted until a visitor turns the switch on. After that:

* **embed.lantern.io** -- the WebAssembly proxy engine, around 12 MB, which is
  why it is fetched on demand from the service rather than bundled into this
  plugin.
* **geo.getiantem.org** -- a country lookup, used to draw the map and to warn
  visitors who appear to be in a censored region that volunteering may not be
  appropriate for them.
* **freddie.iantem.io** -- peer discovery and connection signaling.
* **unbounded.iantem.io** -- the exit servers that relayed traffic egresses
  through. If the widget's page freezes or crashes, a diagnostic report is also
  sent here: timing measurements, the page's address, the browser's user-agent
  string, and the widget build. It carries no identifier for the visitor.
* **netstated-d7bbec1ed55b.herokuapp.com** -- anonymous network-state updates
  that power the live map of active connections.

= What is kept in the visitor's browser =

The widget stores a few values in browser storage: the interface language, a
crash-recovery marker, and -- inside the embed.lantern.io frame, so it follows
the visitor across sites that run the widget -- running totals of people
helped. None of it identifies the visitor.

Unbounded does not track visitors, set advertising identifiers, or collect
personal data. Relayed traffic is end-to-end encrypted between the person being
helped and their destination; a volunteering browser cannot read it.

* Privacy policy: https://lantern.io/privacy
* Terms of service: https://lantern.io/terms

By installing and enabling this plugin you are choosing to load the widget from
these services on the pages you enable it for. Please tell your visitors, and
add it to your own privacy policy if you have one.

== Installation ==

1. Install and activate the plugin.
2. Open **Unbounded** in the admin sidebar and pick a layout, theme, and location.
3. Decide where the widget appears. Either tick **Display on Homepage** or
   **Display on Posts** on that settings screen, or enable it for individual
   pages: edit a page and tick **Enable Unbounded on this page** in the editor
   sidebar.
4. View the page. The widget renders with its switch off, waiting for a visitor
   to opt in.

== Frequently Asked Questions ==

= Does this slow down my site? =

The widget bundle is deferred, so it does not block rendering. The proxy engine
is only downloaded for visitors who turn the switch on.

= Does it use my visitors' bandwidth without asking? =

No. The switch is off on page load and the proxy engine is not downloaded until
a visitor turns it on. Turning it off, navigating away, or closing the tab stops
it.

= Can my visitors get in trouble for volunteering? =

Volunteers relay encrypted traffic; they are not the exit point, so traffic does
not appear to originate from their connection. The widget also detects when a
visitor appears to be in a censored region and warns them before they opt in.
See the privacy policy for details.

= Can I host the widget bundle myself? =

Yes, with the `browsers_unbounded_script_url` filter. Be aware that a
self-hosted copy is pinned to whatever build you copied and will eventually fall
out of step with the network, so the hosted URL is the default.

= Why is the HTML element called "browsers-unbounded"? =

The product used to be called Browsers Unbounded. The element name is the
widget's public API and renaming it would break every existing embed, so it
stayed.

== Changelog ==

= 1.2.0 =
* Prepared for the WordPress Plugin Directory: license and plugin headers, translatable strings, uninstall cleanup.
* Settings are now validated against an allowlist before being saved.
* The widget bundle is registered with `wp_enqueue_script` and can be redirected with the `browsers_unbounded_script_url` filter.
* Added the "simple" layout, which the widget supported but the settings screen did not offer.

= 1.1 =
* Renamed from Browsers Unbounded to Unbounded.
* Fixed a PHP warning and empty widget attributes on a fresh install, before any settings had been saved.

= 1.0 =
* First version.
