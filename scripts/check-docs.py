#!/usr/bin/env python3
"""Check repository Markdown files, local links and heading anchors offline."""
import argparse
from collections import Counter
from pathlib import Path
import re
import sys
import unicodedata
from urllib.parse import unquote, urlsplit


def visible_lines(text):
    """Retain line numbers while ignoring fenced examples and HTML comments."""
    text = re.sub(r"<!--.*?-->", lambda match: "\n" * match[0].count("\n"), text, flags=re.S)
    fence = None
    opening = 0
    lines = []
    for number, line in enumerate(text.splitlines(), 1):
        match = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", line)
        if fence:
            if match and match[1][0] == fence[0] and len(match[1]) >= len(fence) and not match[2].strip():
                fence = None
            continue
        if match:
            fence, opening = match[1], number
            continue
        if not line.startswith(("    ", "\t")):
            lines.append((number, line))
    return lines, opening if fence else None


def heading_slug(value):
    value = re.sub(r"!?(?:\[([^\]]*)\])\([^)]*\)", r"\1", value)
    value = re.sub(r"<[^>]*>", "", value).replace("`", "").replace("*", "").strip().lower()
    value = "".join(char for char in value if char in "-_" or char.isspace() or unicodedata.category(char)[0] in "LMN")
    return re.sub(r"\s", "-", value)


def anchors(text):
    lines, _ = visible_lines(text)
    counts = Counter()
    visible = "\n".join(line for _, line in lines)
    result = set(re.findall(r'<(?:a|h[1-6])\b[^>]*(?:id|name)=["\']([^"\']+)["\']', visible))
    for _, line in lines:
        match = re.match(r"^ {0,3}#{1,6}\s+(.+?)\s*#*\s*$", line)
        if match:
            slug = heading_slug(match[1])
            count = counts[slug]
            result.add(f"{slug}-{count}" if count else slug)
            counts[slug] += 1
    return result


def links_in_line(line):
    # Inline code often demonstrates deliberately invalid client paths or syntax.
    line = re.sub(r"(`+)(.*?)\1", "", line)
    for match in re.finditer(r"!?\[[^\]\n]*\]\((<[^>]+>|[^\s)]+)(?:\s+[^)]*)?\)", line):
        yield match[1].strip("<>")
    match = re.match(r"^ {0,3}\[[^\]]+\]:\s*(<[^>]+>|\S+)", line)
    if match:
        yield match[1].strip("<>")


def markdown_files(root):
    candidates = [*root.glob("*.md"), *root.glob("docs/**/*.md"), *root.glob("fixtures/*.md"), *root.glob(".github/**/*.md")]
    return sorted(set(path for path in candidates if path.is_file()))


def check(root):
    root = Path(root).resolve()
    files = markdown_files(root)
    cached = {}
    findings = []
    link_count = 0
    for path in files:
        text = path.read_text(encoding="utf-8-sig")
        cached[path.resolve()] = anchors(text)
        lines, unfinished = visible_lines(text)
        if unfinished:
            findings.append((path.relative_to(root).as_posix(), unfinished, "unclosed code fence"))
        for number, line in lines:
            for target in links_in_line(line):
                parsed = urlsplit(target)
                if parsed.scheme or parsed.netloc:
                    continue
                link_count += 1
                linked = (path.parent / unquote(parsed.path)).resolve() if parsed.path else path.resolve()
                if parsed.path.startswith("/"):
                    linked = (root / unquote(parsed.path).lstrip("/")).resolve()
                label = path.relative_to(root).as_posix()
                if not linked.is_relative_to(root):
                    findings.append((label, number, "local link escapes repository: " + target))
                elif not linked.exists():
                    findings.append((label, number, "missing local target: " + target))
                elif parsed.fragment and linked.suffix.lower() == ".md":
                    if linked not in cached:
                        cached[linked] = anchors(linked.read_text(encoding="utf-8-sig"))
                    if unquote(parsed.fragment) not in cached[linked]:
                        findings.append((label, number, "missing heading anchor: " + target))
    return files, link_count, findings


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    files, count, findings = check(args.root)
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
    for path, line, message in findings:
        print(f"{path}:{line}: {message}")
    print(f"Documentation check: {len(files)} files, {count} local links, {len(findings)} findings")
    return 1 if findings else 0


if __name__ == "__main__":
    raise SystemExit(main())
