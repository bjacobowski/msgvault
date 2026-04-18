#!/usr/bin/env python3
"""
Decompose HTML email bodies into independent structural layers.

Instead of fingerprinting whole messages (combinatorial explosion),
identifies the ~20-30 atomic patterns that compose in different ways
across 7 dimensions: envelope, layout, quoting, content, decoration,
entities, specials.

Usage:
    python tools/htmlanalysis.py [--db PATH] [--limit N] [--output DIR]
"""

import argparse
import email
import json
import os
import re
import sqlite3
import sys
import zlib
from collections import Counter, defaultdict
from dataclasses import dataclass, field, asdict
from html.parser import HTMLParser
from pathlib import Path
from typing import Optional


# ── DOM analysis ─────────────────────────────────────────────────────────────

class StructureParser(HTMLParser):
    """Single-pass HTML parser that extracts all structural signals."""

    def __init__(self):
        super().__init__()
        self.tags = []  # all tags encountered
        self.tag_stack = []  # current nesting stack
        self.max_depth = 0
        self.table_depth = 0
        self.max_table_depth = 0
        self.blockquote_depth = 0
        self.max_blockquote_depth = 0
        self.inline_style_count = 0
        self.class_count = 0
        self.attrs_seen = set()  # interesting attrs: class values, ids
        self.class_values = []  # all class attribute values
        self.id_values = []
        self.text_chunks = []
        self.link_count = 0
        self.img_count = 0
        self.img_attrs = []  # for tracking pixel detection

    def handle_starttag(self, tag, attrs):
        tag = tag.lower()
        self.tags.append(tag)
        self.tag_stack.append(tag)
        depth = len(self.tag_stack)
        if depth > self.max_depth:
            self.max_depth = depth

        if tag == "table":
            self.table_depth += 1
            if self.table_depth > self.max_table_depth:
                self.max_table_depth = self.table_depth

        if tag == "blockquote":
            self.blockquote_depth += 1
            if self.blockquote_depth > self.max_blockquote_depth:
                self.max_blockquote_depth = self.blockquote_depth

        if tag == "a":
            self.link_count += 1
        if tag == "img":
            self.img_count += 1
            self.img_attrs.append(dict(attrs))

        attr_dict = dict(attrs)
        if "style" in attr_dict:
            self.inline_style_count += 1
        if "class" in attr_dict:
            self.class_count += 1
            self.class_values.append(attr_dict["class"])
        if "id" in attr_dict:
            self.id_values.append(attr_dict["id"])

    def handle_endtag(self, tag):
        tag = tag.lower()
        if tag == "table" and self.table_depth > 0:
            self.table_depth -= 1
        if tag == "blockquote" and self.blockquote_depth > 0:
            self.blockquote_depth -= 1
        # Pop stack (tolerant of mismatched tags)
        if self.tag_stack and self.tag_stack[-1] == tag:
            self.tag_stack.pop()

    def handle_data(self, data):
        stripped = data.strip()
        if stripped:
            self.text_chunks.append(stripped)


# ── Layer classifiers ────────────────────────────────────────────────────────

def classify_envelope(html: str, lower: str) -> str:
    has_doctype = "<!doctype" in lower
    has_html = "<html" in lower
    has_body = "<body" in lower
    if has_doctype and has_html:
        return "full-doctype"
    if has_html:
        return "full-no-doctype"
    if has_body:
        return "body-only"
    return "fragment"


def classify_layout(lower: str, table_depth: int) -> str:
    has_table = "<table" in lower
    has_div = "<div" in lower
    has_p = "<p" in lower or "<p>" in lower
    br_count = lower.count("<br")

    if table_depth >= 3:
        return "table-deep"
    if table_depth == 2:
        return "table-nested"
    if has_table:
        return "table-simple"
    if has_div:
        return "div-based"
    if has_p:
        return "paragraphs"
    if br_count >= 2:
        return "br-delimited"
    return "minimal"


# Pre-compiled quote patterns
_RE_GMAIL_QUOTE = re.compile(r'class=["\']?gmail_quote', re.I)
_RE_OUTLOOK_QUOTE = re.compile(
    r'(class=["\']?OutlookMessageHeader|<!--\s*\[if\s+gte\s+mso)', re.I
)
_RE_MOZ_CITE = re.compile(r'class=["\']?moz-cite', re.I)
_RE_YAHOO_QUOTE = re.compile(r'class=["\']?yahoo_quoted', re.I)
_RE_APPLE_QUOTE = re.compile(
    r'(type=["\']?cite|class=["\']?AppleOriginal)', re.I
)
_RE_ON_WROTE = re.compile(r"On\s+.{10,80}\s+wrote:", re.I)
_RE_FORWARDED = re.compile(
    r"(---------- Forwarded message|Begin forwarded message|"
    r"-----\s*Original Message\s*-----)", re.I
)


def classify_quotes(html: str, lower: str, bq_depth: int) -> tuple[list[str], int]:
    quotes = []
    if _RE_GMAIL_QUOTE.search(html):
        quotes.append("gmail-quote")
    if _RE_OUTLOOK_QUOTE.search(html):
        quotes.append("outlook-quote")
    if _RE_MOZ_CITE.search(html):
        quotes.append("moz-cite")
    if _RE_YAHOO_QUOTE.search(html):
        quotes.append("yahoo-quote")
    if _RE_APPLE_QUOTE.search(html):
        quotes.append("apple-mail-quote")
    if _RE_FORWARDED.search(html):
        quotes.append("forwarded-message")

    # Generic blockquote only if no specific quote class found
    if "<blockquote" in lower and not any(
        q in ("gmail-quote", "moz-cite") for q in quotes
    ):
        quotes.append("blockquote")

    # "On ... wrote:" only if nothing else matched
    if not quotes and _RE_ON_WROTE.search(html):
        quotes.append("generic-on-wrote")

    return quotes, bq_depth


def classify_content(lower: str, parser: StructureParser) -> list[str]:
    patterns = []
    tag_set = set(parser.tags)

    if tag_set & {"ul", "ol", "dl"}:
        patterns.append("lists")
    if tag_set & {"pre", "code"}:
        patterns.append("pre-code")
    if "img" in tag_set:
        patterns.append("images")
    if tag_set & {"h1", "h2", "h3", "h4", "h5", "h6"}:
        patterns.append("headings")
    if "hr" in tag_set:
        patterns.append("hr-dividers")
    if parser.link_count >= 10:
        patterns.append("link-heavy")

    formatting_tags = {"b", "strong", "i", "em", "u", "font", "span"}
    has_formatting = bool(tag_set & formatting_tags) or "color:" in lower or "font-weight" in lower
    if has_formatting:
        patterns.append("rich-text")
    elif not patterns:
        patterns.append("plain-text")

    return patterns


def classify_decoration(lower: str, parser: StructureParser) -> list[str]:
    has_inline = parser.inline_style_count > 0
    has_block = "<style" in lower
    has_class = parser.class_count > 0

    if has_inline and has_block and has_class:
        return ["css-heavy"]

    parts = []
    if has_inline:
        parts.append("inline-style")
    if has_block:
        parts.append("style-block")
    if has_class:
        parts.append("class-based")
    return parts or ["none"]


_RE_NAMED_ENTITY = re.compile(r"&[a-zA-Z]+;")
_RE_NUMERIC_ENTITY = re.compile(r"&#x?[0-9a-fA-F]+;")
_RE_NBSP_RUN = re.compile(r"(&nbsp;){2,}")


def classify_entities(html: str) -> str:
    has_named = bool(_RE_NAMED_ENTITY.search(html))
    has_numeric = bool(_RE_NUMERIC_ENTITY.search(html))
    nbsp_runs = len(_RE_NBSP_RUN.findall(html))

    if nbsp_runs >= 3:
        return "nbsp-heavy"
    if has_named and has_numeric:
        return "mixed"
    if has_numeric:
        return "numeric"
    if has_named:
        return "named-only"
    return "none"


_RE_SIGNATURE = re.compile(
    r'(class=["\']?\S*signature|id=["\']?\S*signature|'
    r'class=["\']?gmail_signature|-- <br)', re.I
)
_RE_MSO_CONDITIONAL = re.compile(r"<!--\s*\[if\s")
_RE_BR_RUN = re.compile(r"(<br\s*/?\s*>[\s]*){3,}", re.I)
_RE_NBSP_SPACING = re.compile(r"(&nbsp;){3,}")


def classify_specials(
    html: str, lower: str, parser: StructureParser
) -> list[str]:
    patterns = []

    if _RE_SIGNATURE.search(html):
        patterns.append("signature")

    # Tracking pixels: 1x1 images
    for attrs in parser.img_attrs:
        w = attrs.get("width", "")
        h = attrs.get("height", "")
        if w in ("1", "0") and h in ("1", "0"):
            patterns.append("tracking-pixel")
            break

    if "unsubscribe" in lower:
        patterns.append("unsubscribe")
    if "<script" in lower:
        patterns.append("script-tags")
    if _RE_MSO_CONDITIONAL.search(html):
        patterns.append("mso-conditional")
    if _RE_BR_RUN.search(lower):
        patterns.append("br-runs")
    if _RE_NBSP_SPACING.search(html):
        patterns.append("nbsp-spacing")
    if "base64" in lower and "<img" in lower:
        patterns.append("base64-images")

    return patterns


# ── Main decomposition ───────────────────────────────────────────────────────

@dataclass
class MessageLayers:
    message_id: int
    size_bytes: int
    envelope: str = ""
    layout: str = ""
    quotes: list[str] = field(default_factory=list)
    quote_depth: int = 0
    content: list[str] = field(default_factory=list)
    decoration: list[str] = field(default_factory=list)
    entities: str = ""
    specials: list[str] = field(default_factory=list)
    max_dom_depth: int = 0
    table_depth: int = 0


def decompose(msg_id: int, html_body: str) -> MessageLayers:
    lower = html_body.lower()

    parser = StructureParser()
    try:
        parser.feed(html_body)
    except Exception:
        pass  # best-effort; malformed HTML is common

    m = MessageLayers(message_id=msg_id, size_bytes=len(html_body))
    m.envelope = classify_envelope(html_body, lower)
    m.layout = classify_layout(lower, parser.max_table_depth)
    m.table_depth = parser.max_table_depth
    m.max_dom_depth = parser.max_depth
    m.quotes, m.quote_depth = classify_quotes(
        html_body, lower, parser.max_blockquote_depth
    )
    m.content = classify_content(lower, parser)
    m.decoration = classify_decoration(lower, parser)
    m.entities = classify_entities(html_body)
    m.specials = classify_specials(html_body, lower, parser)
    return m


# ── Database helpers ─────────────────────────────────────────────────────────

def extract_html(raw_data: bytes, compression: Optional[str]) -> Optional[str]:
    """Decompress raw MIME and extract HTML body."""
    if compression == "zlib":
        try:
            raw_data = zlib.decompress(raw_data)
        except zlib.error:
            return None

    try:
        msg = email.message_from_bytes(raw_data)
    except Exception:
        return None

    # Walk MIME parts for text/html
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
    else:
        if msg.get_content_type() == "text/html":
            payload = msg.get_payload(decode=True)
            if payload:
                charset = msg.get_content_charset() or "utf-8"
                try:
                    return payload.decode(charset, errors="replace")
                except (LookupError, UnicodeDecodeError):
                    return payload.decode("utf-8", errors="replace")
    return None


# ── Output ───────────────────────────────────────────────────────────────────

def write_summary(path: Path, total: int, dim_counts: dict[str, Counter]):
    with open(path, "w", encoding="utf-8") as f:
        f.write(f"=== HTML Body Decomposition ({total:,} messages) ===\n\n")

        dim_names = {
            "envelope": "1. ENVELOPE (document wrapper)",
            "layout": "2. LAYOUT (content structure)",
            "quotes": "3. QUOTING (reply/forward patterns)",
            "content": "4. CONTENT (leaf node types)",
            "decoration": "5. DECORATION (CSS/styling)",
            "entities": "6. ENTITIES (character encoding)",
            "specials": "7. SPECIALS (email-specific patterns)",
        }

        total_atoms = 0
        for dim_key, title in dim_names.items():
            counter = dim_counts[dim_key]
            f.write(f"{'━' * 60}\n")
            f.write(f"  {title}\n")
            f.write(f"{'━' * 60}\n")
            for pattern, count in counter.most_common():
                pct = count / total * 100
                bar = "█" * int(pct / 2)
                f.write(f"  {pattern:<30s} {count:5d} ({pct:4.1f}%) {bar}\n")
            total_atoms += len(counter)
            f.write("\n")

        f.write(f"{'━' * 60}\n")
        f.write(f"  SUMMARY\n")
        f.write(f"{'━' * 60}\n")
        f.write(f"  Total atomic patterns: {total_atoms}\n")
        f.write(f"  Test one per atom:     ~{total_atoms} test cases\n")
        f.write(f"  + top compositions:    ~{total_atoms + 30} test cases\n")
        f.write(f"\n  Compare: naive fingerprinting would yield 600+ archetypes.\n")
        f.write(f"  Decomposition reduces this to {total_atoms} independent atoms.\n")


def write_test_matrix(
    path: Path,
    dim_examples: dict[str, dict[str, list[MessageLayers]]],
    all_layers: list[MessageLayers],
):
    """Pick smallest example per atom + top compositions."""
    atoms = []
    for dim, patterns in sorted(dim_examples.items()):
        for pattern, examples in sorted(
            patterns.items(), key=lambda kv: -len(kv[1])
        ):
            # Pick smallest
            best = min(examples, key=lambda m: m.size_bytes)
            atoms.append(
                {
                    "dimension": dim,
                    "pattern": pattern,
                    "count": len(examples),
                    "example_message_id": best.message_id,
                    "example_size_bytes": best.size_bytes,
                }
            )

    # Top compositions (envelope × layout × first-quote)
    comp_counter: dict[tuple, list[MessageLayers]] = defaultdict(list)
    for m in all_layers:
        q = m.quotes[0] if m.quotes else "none"
        comp_counter[(m.envelope, m.layout, q)].append(m)

    compositions = []
    for key, members in sorted(comp_counter.items(), key=lambda kv: -len(kv[1])):
        if len(members) < 3:
            continue
        best = min(members, key=lambda m: m.size_bytes)
        compositions.append(
            {
                "description": f"{key[0]} + {key[1]} + {key[2]}",
                "envelope": key[0],
                "layout": key[1],
                "quote": key[2],
                "count": len(members),
                "example_message_id": best.message_id,
                "example_size_bytes": best.size_bytes,
            }
        )
    compositions = compositions[:30]

    matrix = {
        "atoms": atoms,
        "compositions": compositions,
        "total_atoms": len(atoms),
        "total_compositions": len(compositions),
    }

    with open(path, "w", encoding="utf-8") as f:
        json.dump(matrix, f, indent=2)


def write_correlations(path: Path, all_layers: list[MessageLayers]):
    """Find which atoms tend to co-occur (non-obvious correlations)."""
    # For each pair of dimensions, count co-occurrences
    results = {}

    # envelope × layout
    counter = Counter()
    for m in all_layers:
        counter[(m.envelope, m.layout)] += 1
    results["envelope_x_layout"] = [
        {"envelope": k[0], "layout": k[1], "count": v}
        for k, v in counter.most_common(20)
    ]

    # layout × quote
    counter = Counter()
    for m in all_layers:
        for q in m.quotes or ["none"]:
            counter[(m.layout, q)] += 1
    results["layout_x_quote"] = [
        {"layout": k[0], "quote": k[1], "count": v}
        for k, v in counter.most_common(20)
    ]

    # quote × special
    counter = Counter()
    for m in all_layers:
        for q in m.quotes or ["none"]:
            for s in m.specials or ["none"]:
                counter[(q, s)] += 1
    results["quote_x_special"] = [
        {"quote": k[0], "special": k[1], "count": v}
        for k, v in counter.most_common(20)
    ]

    # content × decoration
    counter = Counter()
    for m in all_layers:
        for c in m.content:
            for d in m.decoration:
                counter[(c, d)] += 1
    results["content_x_decoration"] = [
        {"content": k[0], "decoration": k[1], "count": v}
        for k, v in counter.most_common(20)
    ]

    with open(path, "w", encoding="utf-8") as f:
        json.dump(results, f, indent=2)


# ── Main ─────────────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--db",
        default=str(Path.home() / ".msgvault" / "msgvault.db"),
        help="path to msgvault.db",
    )
    parser.add_argument("--limit", type=int, default=0, help="limit messages")
    parser.add_argument("--output", default="html_analysis", help="output dir")
    args = parser.parse_args()

    out = Path(args.output)
    out.mkdir(parents=True, exist_ok=True)

    conn = sqlite3.connect(f"file:{args.db}?mode=ro", uri=True)
    cur = conn.cursor()

    query = """
        SELECT mr.message_id, mr.raw_data, mr.compression
        FROM message_raw mr
        JOIN message_bodies mb ON mb.message_id = mr.message_id
        WHERE mb.body_html IS NOT NULL AND mb.body_html != ''
    """
    if args.limit:
        query += f" LIMIT {args.limit}"

    cur.execute(query)

    # Per-dimension counters and example collectors
    dim_counts: dict[str, Counter] = {
        "envelope": Counter(),
        "layout": Counter(),
        "quotes": Counter(),
        "content": Counter(),
        "decoration": Counter(),
        "entities": Counter(),
        "specials": Counter(),
    }
    dim_examples: dict[str, dict[str, list[MessageLayers]]] = {
        k: defaultdict(list) for k in dim_counts
    }

    all_layers: list[MessageLayers] = []
    processed = errors = 0

    while True:
        rows = cur.fetchmany(500)
        if not rows:
            break
        for msg_id, raw_data, compression in rows:
            html_body = extract_html(raw_data, compression)
            if not html_body:
                errors += 1
                continue

            layers = decompose(msg_id, html_body)
            all_layers.append(layers)

            dim_counts["envelope"][layers.envelope] += 1
            dim_examples["envelope"][layers.envelope].append(layers)

            dim_counts["layout"][layers.layout] += 1
            dim_examples["layout"][layers.layout].append(layers)

            dim_counts["entities"][layers.entities] += 1
            dim_examples["entities"][layers.entities].append(layers)

            for q in layers.quotes or ["none"]:
                dim_counts["quotes"][q] += 1
                dim_examples["quotes"][q].append(layers)

            for c in layers.content:
                dim_counts["content"][c] += 1
                dim_examples["content"][c].append(layers)

            for d in layers.decoration:
                dim_counts["decoration"][d] += 1
                dim_examples["decoration"][d].append(layers)

            for s in layers.specials:
                dim_counts["specials"][s] += 1
                dim_examples["specials"][s].append(layers)

            processed += 1
            if processed % 1000 == 0:
                total_atoms = sum(len(c) for c in dim_counts.values())
                print(
                    f"  {processed:,} messages, {total_atoms} atoms so far...",
                    file=sys.stderr,
                )

    conn.close()

    total_atoms = sum(len(c) for c in dim_counts.values())
    print(f"\n=== Done: {processed:,} messages, {errors} errors ===", file=sys.stderr)
    print(f"Total atomic patterns: {total_atoms}", file=sys.stderr)
    for dim, counter in sorted(dim_counts.items()):
        print(f"  {dim}: {len(counter)} patterns", file=sys.stderr)

    # Write outputs
    write_summary(out / "summary.txt", processed, dim_counts)
    print(f"Wrote {out / 'summary.txt'}", file=sys.stderr)

    write_test_matrix(out / "test_matrix.json", dim_examples, all_layers)
    print(f"Wrote {out / 'test_matrix.json'}", file=sys.stderr)

    write_correlations(out / "correlations.json", all_layers)
    print(f"Wrote {out / 'correlations.json'}", file=sys.stderr)

    # Write all decompositions as JSONL
    with open(out / "all_layers.jsonl", "w") as f:
        for m in all_layers:
            json.dump(asdict(m), f)
            f.write("\n")
    print(f"Wrote {out / 'all_layers.jsonl'}", file=sys.stderr)


if __name__ == "__main__":
    main()
