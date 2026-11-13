#!/usr/bin/env python3
"""The FAQ: one list that generates both the visible FAQ section and its
schema.org FAQPage markup in index.html, so the two can't drift apart.

Edit FAQ below, then run: python3 tools/faq.py
Answers are HTML (links allowed); the JSON-LD gets the same text with tags
stripped. Every answer must be backed by ../readme.txt, ../README.md,
../unbounded.php, this directory's README.md, or the repository README.
"""
import html
import json
import re
from pathlib import Path

SITE = "https://unbounded-wp.lantern.io/"
DIRECTORY = "https://wordpress.org/plugins/unbounded-by-lantern/"
SOURCE = "https://github.com/getlantern/unbounded/tree/main/ui/wp-plugin"

FAQ = [
    ("What is Lantern Action Mode for WordPress?",
     "A free WordPress plugin from <a href=\"https://lantern.io\">Lantern</a> that adds "
     "the Action Mode widget to your site. The widget is a switch: a visitor who turns it "
     "on lends a small amount of their bandwidth to relay traffic for someone whose "
     "government blocks the open internet. Visitors install nothing, and no proxy traffic "
     "passes through your server."),
    ("How does this live demo work?",
     "The launch button opens <a href=\"https://playground.wordpress.net\">WordPress "
     "Playground</a>, which runs WordPress compiled to WebAssembly inside your browser. It "
     "installs and activates the plugin zip built from the current source, and lands on a "
     "page with the widget already embedded. Nothing is installed on a server, and closing "
     "the tab throws the whole site away."),
    ("How do I install the plugin on my WordPress site?",
     "Install <a href=\"" + DIRECTORY + "\">Lantern Action Mode</a> from the WordPress "
     "Plugin Directory and activate it. Then open <b>Action Mode</b> in the admin sidebar "
     "to pick a layout, theme and location, and choose where the widget appears: tick "
     "<b>Display on Homepage</b> or <b>Display on Posts</b>, or edit a page and tick "
     "<b>Enable Action Mode on this page</b>. It needs WordPress 5.0 or later and "
     "PHP&nbsp;7.4 or later."),
    ("What can I configure?",
     "The layout (banner, panel, floating or simple), the theme (light, dark, or the "
     "visitor's system preference), whether it sits at the top or bottom of the page, and "
     "where it appears: on chosen pages, on all posts, or on the homepage. Version 1.4.0 "
     "adds a \"Country to support\" setting that preselects a censored country for visitors to help; it takes effect "
     "only once Lantern switches that feature on for the network."),
    ("What happens in a visitor's browser when they turn it on?",
     "Their browser downloads the WebAssembly proxy engine, around 12&nbsp;MB, from "
     "embed.lantern.io, finds people to help through Lantern's signaling server, and "
     "relays their traffic to Lantern's exit servers. Relayed traffic is end-to-end "
     "encrypted between the person being helped and their destination, so the volunteering "
     "browser cannot read it. Turning the switch off, navigating away or closing the tab "
     "stops it."),
    ("Does it use my visitors' bandwidth without asking?",
     "No. The switch is off when the page loads, and the proxy engine is not downloaded "
     "until a visitor turns it on."),
    ("Does Action Mode slow down my site?",
     "The widget bundle is deferred, so it does not block rendering. The proxy engine is "
     "only downloaded for visitors who turn the switch on."),
    ("Can my visitors get in trouble for volunteering?",
     "Volunteers relay encrypted traffic and are not the exit point, so the traffic does "
     "not appear to come from their connection. The widget also warns visitors who appear "
     "to be in a censored region that volunteering may not be appropriate for them."),
    ("What data does the plugin collect?",
     "Action Mode does not track visitors, set advertising identifiers or collect personal "
     "data. The widget sends cookieless usage counts to Plausible, tagged with the hostname "
     "of your site, and those counts rank sites on the "
     "<a href=\"https://actionmode.lantern.io/leaderboard\">Action Mode leaderboard</a>. "
     "The External services section of the plugin's "
     "<a href=\"https://github.com/getlantern/unbounded/blob/main/ui/wp-plugin/readme.txt\">readme</a> "
     "lists each service the widget contacts and when. See Lantern's "
     "<a href=\"https://lantern.io/privacy\">privacy policy</a>."),
    ("Can I host the widget bundle myself?",
     "Yes, with the <code>browsers_unbounded_script_url</code> filter. A self-hosted copy "
     "is pinned to whatever build you copied and will eventually fall out of step with the "
     "network, so the bundle on embed.lantern.io is the default."),
    ("What is the relationship between Action Mode and Unbounded?",
     "<a href=\"https://github.com/getlantern/unbounded\">Unbounded</a> is Lantern's "
     "engine for censorship circumvention through browser-based peer-to-peer proxies. "
     "The volunteer product built on it, including this plugin, ships as Lantern Action "
     "Mode. The product was called Browsers Unbounded, then Unbounded, and is now Lantern "
     "Action Mode."),
    ("How is Action Mode different from Tor's Snowflake?",
     "Both descend from the flash proxy idea that Fifield et al. described in 2012: "
     "volunteers' browsers act as short-lived proxies for people in censored regions. "
     "<a href=\"https://snowflake.torproject.org/\">Snowflake</a> is part of the Tor "
     "Project. Action Mode is built on Lantern's Unbounded, and one volunteer can proxy "
     "for several users at the same time."),
    ("Is the plugin open source?",
     "Yes. It is licensed under the GPL, version 3 or later, and developed in the open in "
     "<a href=\"" + SOURCE + "\">getlantern/unbounded</a>. The plugin is in "
     "<code>ui/wp-plugin</code> and the widget it loads is in <code>ui/</code>."),
    ("Who makes Lantern Action Mode?",
     "<a href=\"https://lantern.io\">Lantern</a>, run by Brave New Software Project, Inc. "
     "If you need to get around censorship yourself, "
     "<a href=\"https://lantern.io/download\">get Lantern</a>."),
]


def strip(s: str) -> str:
    return html.unescape(re.sub(r"<[^>]+>", "", s)).replace(" ", " ")


def faq_html() -> str:
    items = []
    for q, a in FAQ:
        slug = re.sub(r"[^a-z0-9]+", "-", q.lower()).strip("-")
        items.append(
            f'    <div class="qa" id="{slug}">\n'
            f'      <h3>{html.escape(q)}</h3>\n'
            f'      <p>{a}</p>\n'
            f'    </div>'
        )
    return "\n".join(items)


def faq_jsonld() -> dict:
    return {
        "@type": "FAQPage",
        "@id": SITE + "#faq",
        "isPartOf": {"@id": SITE + "#webpage"},
        "mainEntity": [
            {"@type": "Question", "name": q,
             "acceptedAnswer": {"@type": "Answer", "text": strip(a)}}
            for q, a in FAQ
        ],
    }


def graph() -> dict:
    org = {
        "@type": "Organization",
        "@id": "https://lantern.io/#organization",
        "name": "Lantern",
        "legalName": "Brave New Software Project, Inc.",
        "url": "https://lantern.io",
        "sameAs": ["https://github.com/getlantern"],
    }
    return {
        "@context": "https://schema.org",
        "@graph": [
            org,
            {
                "@type": "WebSite",
                "@id": SITE + "#website",
                "url": SITE,
                "name": "Lantern Action Mode for WordPress",
                "publisher": {"@id": org["@id"]},
                "inLanguage": "en",
            },
            {
                "@type": "WebPage",
                "@id": SITE + "#webpage",
                "url": SITE,
                "name": "Lantern Action Mode for WordPress: live plugin demo",
                "isPartOf": {"@id": SITE + "#website"},
                "about": {"@id": SITE + "#software"},
                "primaryImageOfPage": SITE + "og.png",
                "inLanguage": "en",
            },
            {
                "@type": "SoftwareApplication",
                "@id": SITE + "#software",
                "name": "Lantern Action Mode",
                "description": strip(FAQ[0][1]),
                "applicationCategory": "WebApplication",
                "applicationSubCategory": "WordPress plugin",
                "operatingSystem": "WordPress 5.0 or later, PHP 7.4 or later",
                "downloadUrl": DIRECTORY,
                "installUrl": DIRECTORY,
                "sameAs": [DIRECTORY],
                "license": "https://www.gnu.org/licenses/gpl-3.0.html",
                "offers": {"@type": "Offer", "price": "0", "priceCurrency": "USD"},
                "url": SITE,
                "author": {"@id": org["@id"]},
                "publisher": {"@id": org["@id"]},
                "keywords": "WordPress plugin, censorship circumvention, volunteer proxy, "
                            "peer-to-peer proxy, WebRTC, Unbounded, Browsers Unbounded",
            },
            faq_jsonld(),
        ],
    }


def main() -> None:
    p = Path(__file__).resolve().parent.parent / "index.html"
    s = p.read_text()
    s = re.sub(r"(<!-- faq:start -->).*?(\n[ \t]*<!-- faq:end -->)",
               lambda m: m.group(1) + "\n" + faq_html() + m.group(2), s, flags=re.S)
    ld = json.dumps(graph(), ensure_ascii=False, indent=1)
    s = re.sub(r'(<script type="application/ld\+json">\n).*?(\n</script>)',
               lambda m: m.group(1) + ld + m.group(2), s, flags=re.S)
    p.write_text(s)


if __name__ == "__main__":
    main()
