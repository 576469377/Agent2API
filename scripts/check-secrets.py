#!/usr/bin/env python3
"""Check Git content for credential files and obvious secrets; never print values."""
import argparse
import io
import json
from pathlib import PurePosixPath
import re
import subprocess


def git(*args):
    return subprocess.check_output(["git", *args])


def forbidden_path(path):
    p = PurePosixPath(path.lower())
    name = p.name
    if set(p.parts) & {"auths", ".workbuddy", "bin", ".zcode", ".cc-switch"}:
        return True
    if name.startswith("cc-switch.db"):
        return True
    if ".pi" in p.parts and (name.startswith(("models.json", "auth.json")) or "/.pi/agent/" in "/" + p.as_posix()):
        return True
    if name in {"config.example.json", ".env.example", ".env.sample"}:
        return False
    return bool(re.search(
        r"^(?:config(?:\.[^/]+)?\.json(?:\..*)?|provider_config\.json.*|.*\.session\.json.*|"
        r".*\.info(?:\..*)?|.*credential.*\.json(?:\..*)?|cred.*\.json(?:\..*)?|"
        r"(?:agent2api-)?metrics\.json(?:\..*)?|\.env(?:\..*)?|.*\.env(?:\..*)?|"
        r".*\.(?:pem|key|candidate|backup)|.*\.bak(?:-.*)?)$", name))


PATTERNS = [
    ("private key", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----")),
    ("GitHub token", re.compile(r"\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})\b")),
    ("API token", re.compile(r"\bsk-[A-Za-z0-9_-]{24,}\b")),
    ("AWS access key", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
    ("JWT", re.compile(r"\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]+")),
]
FIELDS = re.compile(r'''["']?(?:access_?token|refresh_?token|api_?key|client_?secret|password)["']?\s*[:=]\s*["']([^"'\r\n]+)["']''', re.I)
ENV_FIELDS = re.compile(r'''^\s*(?:export\s+)?(?:[A-Z0-9]+_)*(?:ACCESS_TOKEN|REFRESH_TOKEN|API_KEY|CLIENT_SECRET|PASSWORD)\s*=\s*([^\s"'#]+)''')
PLACEHOLDER = re.compile(r"(?:<[^<>\r\n]+>|\$\{[A-Z_][A-Z0-9_]*\}|\$[A-Z_][A-Z0-9_]*|YOUR_[A-Z0-9_]+)", re.I)


def decode_content(data):
    if data.startswith((b'\xff\xfe\x00\x00', b'\x00\x00\xfe\xff')):
        return data.decode('utf-32', errors='replace')
    if data.startswith((b'\xff\xfe', b'\xfe\xff')):
        return data.decode('utf-16', errors='replace')
    # BOM-less UTF-16 is common in Windows exports. Also scan ordinary binary
    # bytes: a NUL must not suppress a readable credential elsewhere in a file.
    if len(data) >= 8:
        sample = data[:4096]
        if sample[1::2].count(0) > len(sample) // 4:
            return data.decode('utf-16-le', errors='replace')
        if sample[::2].count(0) > len(sample) // 4:
            return data.decode('utf-16-be', errors='replace')
    return data.decode('utf-8-sig', errors='replace')


def structured_credentials(value):
    if isinstance(value, dict):
        for key, child in value.items():
            if re.fullmatch(r'access_?token|refresh_?token|api_?key|client_?secret|password', key, re.I):
                if isinstance(child, str) and len(child) >= 16 and not PLACEHOLDER.fullmatch(child):
                    yield "credential field"
                if isinstance(child, dict) and child.get('$wbEncrypted') == 1 and len(str(child.get('envelope', ''))) >= 16:
                    yield "encrypted credential"
            yield from structured_credentials(child)
    elif isinstance(value, list):
        for child in value:
            yield from structured_credentials(child)


def content_findings(data):
    text = decode_content(data)
    findings = text_findings(text)
    raw_text = data.decode('utf-8-sig', errors='replace')
    if raw_text != text:
        # Encoding heuristics must not hide readable tokens in binary payloads.
        findings.extend(text_findings(raw_text))
    return sorted(set(findings))


def text_findings(text):
    findings = []
    try:
        findings.extend((1, label) for label in set(structured_credentials(json.loads(text))))
    except (ValueError, RecursionError):
        pass
    for number, line in enumerate(text.splitlines(), 1):
        for label, pattern in PATTERNS:
            if pattern.search(line):
                findings.append((number, label))
        for match in (*FIELDS.finditer(line), *ENV_FIELDS.finditer(line)):
            value = match[1]
            if len(value) >= 16 and not PLACEHOLDER.fullmatch(value):
                findings.append((number, "credential field"))
    return findings


def objects(mode):
    if mode == "history":
        # rev-list associates each blob with only one path. Inspect path changes
        # separately so copied/renamed credential files cannot hide behind a
        # harmless name for the same blob. -m includes merge resolutions.
        paths = git("log", "--all", "--format=", "--name-only", "--no-renames", "--root", "-m", "-z")
        for path in sorted(set(paths.decode("utf-8").split("\0"))):
            path = path.lstrip("\n")
            if path and forbidden_path(path):
                yield path, "history-path", b""
        # Scan all reachable blobs once, including files deleted by later commits.
        entries = [line.partition(" ") for line in git("rev-list", "--objects", "--all").decode("utf-8").splitlines()]
        entries = [(oid, path) for oid, _, path in entries if path]
        payload = "".join(oid + "\n" for oid, _ in entries).encode()
        batch = io.BytesIO(subprocess.check_output(["git", "cat-file", "--batch"], input=payload))
        for oid, path in entries:
            header = batch.readline().split()
            if len(header) != 3:
                raise RuntimeError("Unable to read Git object " + oid)
            data = batch.read(int(header[2]))
            batch.read(1)
            if header[1] == b"blob":
                yield path, oid, data
    else:
        args = ("diff", "--cached", "--no-renames", "--name-only", "--diff-filter=ACM", "-z") if mode == "staged" else ("ls-files", "-z")
        for path in git(*args).decode("utf-8").split("\0"):
            if path:
                # Read the index, not the working tree; partial staging must be safe.
                yield path, "index", git("show", ":" + path)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--staged", action="store_true")
    group.add_argument("--tracked", action="store_true")
    group.add_argument("--history", action="store_true")
    args = parser.parse_args()
    mode = "history" if args.history else "tracked" if args.tracked else "staged"
    count = failures = 0
    for path, oid, data in objects(mode):
        count += 1
        if forbidden_path(path):
            print(f"BLOCKED {path} ({oid[:12]}): local credential/runtime path")
            failures += 1
        for line, label in content_findings(data):
            print(f"BLOCKED {path}:{line} ({oid[:12]}): {label}; value redacted")
            failures += 1
    print(f"Secret check: {count} objects, {failures} findings ({mode})")
    return int(bool(failures))


if __name__ == "__main__":
    raise SystemExit(main())
