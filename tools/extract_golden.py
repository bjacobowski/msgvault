#!/usr/bin/env python3
"""
Extract HTML test fixtures from the archive, replacing real content with
placeholder filler while preserving all structural patterns.

Reads test_matrix.json from htmlanalysis.py output, extracts matching HTML
bodies, sanitizes them, and writes golden test files.

Sanitization rules:
- Text nodes: replaced with short filler ("Lorem ipsum dolor sit amet.")
- Email addresses: replaced with user1@example.com, user2@example.com, etc.
- URLs: replaced with https://example.com/link1, /link2, etc.
- Names in From/To-style headers: replaced with "Alice", "Bob", etc.
- Dates in "On ... wrote:" headers: replaced with generic date
- Image src: replaced with placeholder (preserving data: URIs structure)
- Preserve: all tags, attributes (class, id, style), entities, whitespace
  patterns, structural markers (gmail_quote, OutlookMessageHeader, etc.)

Usage:
    python tools/extract_golden.py --analysis html_analysis_full --output internal/mime/testdata/golden
"""

import argparse
import email
import json
import os
import re
import sqlite3
import zlib
from dataclasses import dataclass
from html.parser import HTMLParser
from pathlib import Path
from typing import Optional


# ── Content sanitization ─────────────────────────────────────────────────────

# Filler text fragments, cycled through for variety
FILLERS = [
    "Lorem ipsum dolor sit amet.",
    "Sed do eiusmod tempor incididunt.",
    "Ut enim ad minim veniam.",
    "Duis aute irure dolor in reprehenderit.",
    "Excepteur sint occaecat cupidatat.",
]

NAMES = ["Alice", "Bob", "Carol", "Dave", "Eve", "Frank", "Grace", "Heidi"]


class Sanitizer:
    """Stateful sanitizer that tracks replacements for consistency."""

    def __init__(self):
        self.email_map: dict[str, str] = {}
        self.url_counter = 0
        self.filler_idx = 0
        self.name_idx = 0

    def next_filler(self) -> str:
        f = FILLERS[self.filler_idx % len(FILLERS)]
        self.filler_idx += 1
        return f

    def sanitize_email(self, addr: str) -> str:
        lower = addr.lower()
        if lower not in self.email_map:
            n = len(self.email_map) + 1
            self.email_map[lower] = f"user{n}@example.com"
        return self.email_map[lower]

    def sanitize_url(self, url: str) -> str:
        # Preserve structural URL patterns
        if url.startswith("mailto:"):
            email_part = url[7:].split("?")[0]
            return f"mailto:{self.sanitize_email(email_part)}"
        if url.startswith("data:"):
            # Preserve data: URI structure but truncate content
            # e.g. data:image/gif;base64,R0lGOD... → data:image/gif;base64,AAAA
            if ";base64," in url:
                prefix = url[: url.index(";base64,") + 8]
                return prefix + "AAAA"
            return url[:50]
        if url.startswith("#"):
            return url  # internal anchor, keep as-is
        if url.startswith("cid:"):
            return "cid:part1@local"  # sanitize MIME content-ID
        self.url_counter += 1
        return f"https://example.com/link{self.url_counter}"

    def next_name(self) -> str:
        n = NAMES[self.name_idx % len(NAMES)]
        self.name_idx += 1
        return n


# Email pattern: anything@domain.tld
_RE_EMAIL = re.compile(r"[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}")

# "On <date> <name> wrote:" pattern
_RE_ON_WROTE = re.compile(
    r"(On\s+).{10,80}(\s+wrote:)",
    re.IGNORECASE,
)

# Forwarded message header
_RE_FORWARDED = re.compile(
    r"(---------- Forwarded message ---------\s*)"
    r"(From:.*?Subject:.*?)(\n|\r\n|\Z)",
    re.DOTALL | re.IGNORECASE,
)


class SanitizingHTMLRewriter(HTMLParser):
    """
    Parse HTML and rewrite it with sanitized content.

    Preserves:
    - All tags and their nesting
    - Structural class/id values (gmail_quote, OutlookMessageHeader, etc.)
    - style attributes (structure of CSS)
    - Entity patterns (&nbsp; runs, &#160;, etc.)
    - <br> patterns
    - Comment structures (<!--[if mso]>, etc.)

    Replaces:
    - Text node content with filler
    - Email addresses with fake ones
    - href/src URLs with example.com
    - alt text with generic text
    """

    # Tags whose text content should be preserved or handled specially
    SKIP_CONTENT_TAGS = {"style", "script"}

    # Structural attributes to preserve as-is
    STRUCTURAL_ATTRS = {"class", "id", "dir", "lang", "role", "type", "name"}

    # Attributes with URLs to sanitize
    URL_ATTRS = {"href", "src", "action", "background"}

    def __init__(self, sanitizer: Sanitizer):
        super().__init__(convert_charrefs=False)  # preserve entities
        self.san = sanitizer
        self.output: list[str] = []
        self.tag_stack: list[str] = []
        self.in_skip_tag = 0  # depth inside style/script

    def get_output(self) -> str:
        return "".join(self.output)

    def handle_decl(self, decl: str):
        self.output.append(f"<!{decl}>")

    def handle_comment(self, data: str):
        # Preserve MSO conditionals and structural comments
        self.output.append(f"<!--{data}-->")

    def handle_pi(self, data: str):
        self.output.append(f"<?{data}>")

    def handle_starttag(self, tag: str, attrs: list[tuple[str, Optional[str]]]):
        self.tag_stack.append(tag.lower())
        if tag.lower() in self.SKIP_CONTENT_TAGS:
            self.in_skip_tag += 1

        sanitized_attrs = self._sanitize_attrs(tag.lower(), attrs)
        attr_str = self._format_attrs(sanitized_attrs)
        self.output.append(f"<{tag}{attr_str}>")

    def handle_endtag(self, tag: str):
        if tag.lower() in self.SKIP_CONTENT_TAGS and self.in_skip_tag > 0:
            self.in_skip_tag -= 1
        if self.tag_stack and self.tag_stack[-1] == tag.lower():
            self.tag_stack.pop()
        self.output.append(f"</{tag}>")

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, Optional[str]]]):
        sanitized_attrs = self._sanitize_attrs(tag.lower(), attrs)
        attr_str = self._format_attrs(sanitized_attrs)
        self.output.append(f"<{tag}{attr_str} />")

    def handle_data(self, data: str):
        if self.in_skip_tag > 0:
            # Inside <style>/<script>: sanitize CSS/JS minimally
            self.output.append(self._sanitize_style_content(data))
            return

        # Replace text content
        sanitized = self._sanitize_text(data)
        self.output.append(sanitized)

    def handle_entityref(self, name: str):
        # Preserve all named entities as-is
        self.output.append(f"&{name};")

    def handle_charref(self, name: str):
        # Preserve all numeric entities as-is
        self.output.append(f"&#{name};")

    def _sanitize_attrs(
        self, tag: str, attrs: list[tuple[str, Optional[str]]]
    ) -> list[tuple[str, Optional[str]]]:
        result = []
        for name, value in attrs:
            name_lower = name.lower()

            if value is None:
                result.append((name, value))
                continue

            if name_lower in self.STRUCTURAL_ATTRS:
                # Keep structural attrs as-is (class names, ids are important)
                result.append((name, value))
            elif name_lower in self.URL_ATTRS:
                result.append((name, self.san.sanitize_url(value)))
            elif name_lower == "style":
                # Keep style structure, sanitize URLs inside it
                result.append((name, self._sanitize_style_attr(value)))
            elif name_lower == "alt":
                result.append((name, "image"))
            elif name_lower == "title":
                result.append((name, "title"))
            elif name_lower in ("width", "height", "border", "cellpadding",
                                "cellspacing", "colspan", "rowspan", "valign",
                                "align", "bgcolor", "color", "size", "face",
                                "target", "rel", "media", "charset",
                                "http-equiv", "content", "xmlns", "xmlns:v",
                                "xmlns:o", "xmlns:w", "xmlns:m", "xmlns:x"):
                # Dimensional/structural attrs: preserve
                result.append((name, value))
            else:
                # Unknown attrs: keep name, genericize value if it looks like content
                if _RE_EMAIL.search(value):
                    value = _RE_EMAIL.sub(
                        lambda m: self.san.sanitize_email(m.group()), value
                    )
                result.append((name, value))

        return result

    def _format_attrs(self, attrs: list[tuple[str, Optional[str]]]) -> str:
        if not attrs:
            return ""
        parts = []
        for name, value in attrs:
            if value is None:
                parts.append(f" {name}")
            else:
                # Use double quotes, escape inner quotes
                escaped = value.replace("&", "&amp;").replace('"', "&quot;")
                parts.append(f' {name}="{escaped}"')
        return "".join(parts)

    def _sanitize_text(self, text: str) -> str:
        """Replace text content while preserving whitespace structure."""
        # Pure whitespace: preserve exactly
        if not text.strip():
            return text

        # Check for "On ... wrote:" pattern
        m = _RE_ON_WROTE.search(text)
        if m:
            return _RE_ON_WROTE.sub(
                r"\g<1>Mon, 1 Jan 2024 at 10:00 AM Alice\2", text
            )

        # Replace email addresses first
        text_out = _RE_EMAIL.sub(
            lambda m: self.san.sanitize_email(m.group()), text
        )

        # If the text is very short (likely a label, button, etc.), use short filler
        stripped = text_out.strip()
        if len(stripped) <= 3:
            return text  # keep very short text (punctuation, numbers)

        # Preserve leading/trailing whitespace pattern
        leading = text[: len(text) - len(text.lstrip())]
        trailing = text[len(text.rstrip()) :]

        # Replace content with filler
        filler = self.san.next_filler()

        return leading + filler + trailing

    def _sanitize_style_content(self, css: str) -> str:
        """Sanitize CSS while preserving structure."""
        # Replace URLs in CSS
        css = re.sub(
            r"url\(['\"]?([^)]+?)['\"]?\)",
            lambda m: f"url('{self.san.sanitize_url(m.group(1))}')",
            css,
        )
        return css

    def _sanitize_style_attr(self, style: str) -> str:
        """Sanitize inline style, preserving CSS properties."""
        # Replace URLs
        style = re.sub(
            r"url\(['\"]?([^)]+?)['\"]?\)",
            lambda m: f"url('{self.san.sanitize_url(m.group(1))}')",
            style,
        )
        return style


def sanitize_html(html_body: str) -> str:
    """Sanitize an HTML body, preserving structure but replacing content."""
    san = Sanitizer()
    rewriter = SanitizingHTMLRewriter(san)
    try:
        rewriter.feed(html_body)
    except Exception:
        # If parsing fails, do basic regex sanitization
        return _fallback_sanitize(html_body, san)
    return rewriter.get_output()


def _fallback_sanitize(html: str, san: Sanitizer) -> str:
    """Regex-based fallback for unparseable HTML."""
    html = _RE_EMAIL.sub(lambda m: san.sanitize_email(m.group()), html)
    # Replace text between tags with filler
    html = re.sub(
        r"(>)([^<]{20,})(<)",
        lambda m: m.group(1) + san.next_filler() + m.group(3),
        html,
    )
    return html


# ── Database extraction ──────────────────────────────────────────────────────

def extract_html(raw_data: bytes, compression: Optional[str]) -> Optional[str]:
    if compression == "zlib":
        try:
            raw_data = zlib.decompress(raw_data)
        except zlib.error:
            return None

    try:
        msg = email.message_from_bytes(raw_data)
    except Exception:
        return None

    if msg.is_multipart():
        for part in msg.walk():
            if part.get_content_type() == "text/html":
                payload = part.get_payload(decode=True)
                if payload:
                    charset = part.get_content_charset() or "utf-8"
                    try:
                        return payload.decode(charset, errors="replace")
                    except (LookupError, UnicodeDecodeError):
                        return payload.decode("utf-8", errors="replace")
    elif msg.get_content_type() == "text/html":
        payload = msg.get_payload(decode=True)
        if payload:
            charset = msg.get_content_charset() or "utf-8"
            try:
                return payload.decode(charset, errors="replace")
            except (LookupError, UnicodeDecodeError):
                return payload.decode("utf-8", errors="replace")
    return None


# ── Main ─────────────────────────────────────────────────────────────────────

@dataclass
class TestFixture:
    filename: str
    dimension: str
    pattern: str
    category: str  # "atom" or "composition"
    count: int
    original_size: int
    sanitized_size: int


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--analysis",
        default="html_analysis_full",
        help="path to htmlanalysis.py output dir",
    )
    parser.add_argument(
        "--db",
        default=str(Path.home() / ".msgvault" / "msgvault.db"),
        help="path to msgvault.db",
    )
    parser.add_argument(
        "--output",
        default="internal/mime/testdata/golden",
        help="output directory for golden files",
    )
    args = parser.parse_args()

    out = Path(args.output)
    out.mkdir(parents=True, exist_ok=True)

    # Load test matrix
    with open(Path(args.analysis) / "test_matrix.json") as f:
        matrix = json.load(f)

    # Collect all message IDs we need
    msg_ids: dict[int, list[dict]] = {}  # msg_id → list of test case metadata

    for a in matrix["atoms"]:
        mid = a["example_message_id"]
        msg_ids.setdefault(mid, []).append(
            {
                "category": "atom",
                "dimension": a["dimension"],
                "pattern": a["pattern"],
                "count": a["count"],
            }
        )

    for c in matrix["compositions"]:
        mid = c["example_message_id"]
        msg_ids.setdefault(mid, []).append(
            {
                "category": "composition",
                "dimension": "composition",
                "pattern": c["description"],
                "count": c["count"],
            }
        )

    print(f"Extracting {len(msg_ids)} unique messages...", flush=True)

    # Fetch from DB
    conn = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    cur = conn.cursor()

    placeholders = ",".join("?" * len(msg_ids))
    cur.execute(
        f"""SELECT mr.message_id, mr.raw_data, mr.compression
            FROM message_raw mr
            WHERE mr.message_id IN ({placeholders})""",
        list(msg_ids.keys()),
    )

    html_bodies: dict[int, str] = {}
    for msg_id, raw_data, compression in cur.fetchall():
        html = extract_html(raw_data, compression)
        if html:
            html_bodies[msg_id] = html

    conn.close()
    print(f"Extracted {len(html_bodies)} HTML bodies", flush=True)

    # Sanitize and write
    fixtures: list[TestFixture] = []
    used_filenames: set[str] = set()

    for msg_id, metadata_list in sorted(msg_ids.items()):
        html = html_bodies.get(msg_id)
        if not html:
            print(f"  WARNING: no HTML for message {msg_id}")
            continue

        # Generate filename from first (primary) test case
        meta = metadata_list[0]
        base = f"{meta['dimension']}_{meta['pattern']}"
        base = re.sub(r"[^a-zA-Z0-9_\-]", "_", base)
        base = re.sub(r"_+", "_", base).strip("_").lower()

        # Deduplicate filenames
        filename = base + ".html"
        if filename in used_filenames:
            i = 2
            while f"{base}_{i}.html" in used_filenames:
                i += 1
            filename = f"{base}_{i}.html"
        used_filenames.add(filename)

        # Sanitize
        sanitized = sanitize_html(html)

        # Write HTML file
        filepath = out / filename
        filepath.write_text(sanitized, encoding="utf-8")

        for meta in metadata_list:
            fixtures.append(
                TestFixture(
                    filename=filename,
                    dimension=meta["dimension"],
                    pattern=meta["pattern"],
                    category=meta["category"],
                    count=meta["count"],
                    original_size=len(html),
                    sanitized_size=len(sanitized),
                )
            )

        print(
            f"  {filename:50s}  {len(html):>6,} -> {len(sanitized):>6,} bytes  "
            f"({len(metadata_list)} test case(s))"
        )

    # Write manifest
    manifest = {
        "description": "Golden test fixtures for HTML body parsing (StripHTML, etc.)",
        "generated_by": "tools/extract_golden.py",
        "source": "Sanitized real emails — structure preserved, content replaced",
        "fixtures": [
            {
                "filename": f.filename,
                "dimension": f.dimension,
                "pattern": f.pattern,
                "category": f.category,
                "prevalence": f.count,
                "original_size": f.original_size,
                "sanitized_size": f.sanitized_size,
            }
            for f in fixtures
        ],
    }

    manifest_path = out / "manifest.json"
    with open(manifest_path, "w") as f:
        json.dump(manifest, f, indent=2)

    print(f"\nWrote {len(used_filenames)} fixture files + manifest to {out}/")
    print(f"Total fixtures: {len(fixtures)} (some files cover multiple atoms)")


if __name__ == "__main__":
    main()
