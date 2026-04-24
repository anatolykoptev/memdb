#!/usr/bin/env python3
"""Sync CHANGELOG.md from a published GitHub Release body.

Drops release-drafter cruft (``## What's Changed`` block, ``**Full
Changelog:**`` footer), demotes ``##`` category headers to ``###``,
inserts a ``## [<version>] - <YYYY-MM-DD>`` section after
``## [Unreleased]``, and prepends a footer link mapping ``[<version>]``
to the GitHub release tag URL.

Idempotency (``--idempotent``): if a ``## [<version>]`` heading is
already present, exits 0 with ``status=noop``.

Release body source: ``--body-file PATH`` or ``BODY`` env var (workaround
for special chars in YAML inline interpolation).

Exit status:
    0 - success (file written or noop in idempotent mode)
    1 - usage / I/O error
    2 - input malformed (e.g. CHANGELOG missing ``## [Unreleased]``)
"""

from __future__ import annotations

import argparse
import dataclasses
import os
import pathlib
import re
import sys
from typing import Optional

GITHUB_REPO_DEFAULT = "anatolykoptev/memdb"
UNRELEASED_HEADER = "## [Unreleased]"


@dataclasses.dataclass
class SyncResult:
    status: str  # "written", "noop"
    new_changelog: str
    section: str  # the new section text we inserted (for diagnostics)


def clean_release_body(body: str) -> str:
    """Strip release-drafter cruft and demote category headers.

    - Drop leading ``## What's Changed`` block (everything up to the next
      ``##`` header). The bullets under "What's Changed" are uncategorised
      entries that release-drafter could not bucket; we drop them too
      because they are duplicated inside the categorised sections in
      virtually all real releases. Operator can hand-edit the PR if a
      truly unique bullet existed.
    - Drop trailing ``**Full Changelog:** ...`` line(s).
    - Demote ``## Foo`` headers to ``### Foo``.
    - Collapse 3+ blank lines to 2.
    """
    text = body.replace("\r\n", "\n").strip()

    # Drop the "## What's Changed" block. Use a regex that consumes from the
    # header through (but not including) the next ``## `` header or EOF.
    text = re.sub(
        r"^##\s+What['’]s Changed\s*\n.*?(?=^##\s|\Z)",
        "",
        text,
        count=1,
        flags=re.MULTILINE | re.DOTALL,
    )

    # Drop the "**Full Changelog:** ..." footer (one line, possibly with
    # trailing whitespace).
    text = re.sub(
        r"^\*\*Full Changelog:\*\*.*$",
        "",
        text,
        flags=re.MULTILINE,
    )

    # Demote ``## X`` -> ``### X`` (only the level-2 lines; level-3+ stays).
    text = re.sub(r"^##\s+", "### ", text, flags=re.MULTILINE)

    # Collapse runs of blank lines.
    text = re.sub(r"\n{3,}", "\n\n", text).strip()
    return text


def parse_date(published_at: str) -> str:
    """Extract YYYY-MM-DD from an ISO8601 timestamp."""
    # release.published_at looks like "2026-04-24T06:53:18Z" - take the
    # first 10 chars. Fall back to the raw value if it does not match.
    if re.match(r"^\d{4}-\d{2}-\d{2}", published_at):
        return published_at[:10]
    return published_at


def normalise_version(tag: str) -> str:
    """``v2.1.0`` -> ``2.1.0``; idempotent on already-clean inputs."""
    return tag[1:] if tag.startswith("v") else tag


def build_section(version: str, date: str, cleaned_body: str) -> str:
    return f"## [{version}] - {date}\n\n{cleaned_body}\n"


def insert_section(
    existing: str,
    section: str,
    version: str,
    repo: str,
    tag: str,
) -> str:
    """Insert ``section`` after ``## [Unreleased]`` and update footer links."""
    if UNRELEASED_HEADER not in existing:
        raise ValueError(
            f"CHANGELOG.md does not contain '{UNRELEASED_HEADER}'; "
            "cannot determine where to insert new section.",
        )

    # Split into body + footer link table. The footer is a contiguous block
    # of lines matching ``[name]: url`` at the end of the file.
    lines = existing.split("\n")
    footer_start = len(lines)
    for i in range(len(lines) - 1, -1, -1):
        ln = lines[i].strip()
        if not ln:
            continue
        if re.match(r"^\[[^\]]+\]:\s+\S+", ln):
            footer_start = i
            continue
        break

    body_text = "\n".join(lines[:footer_start]).rstrip() + "\n"
    footer_lines = [ln for ln in lines[footer_start:] if ln.strip()]

    # Insert new section right after the Unreleased header. We re-emit the
    # Unreleased header (empty) so future syncs still find it.
    pattern = re.compile(rf"({re.escape(UNRELEASED_HEADER)}\s*\n)", re.MULTILINE)
    if not pattern.search(body_text):
        raise ValueError("Internal: lost Unreleased header during split.")

    replacement = f"{UNRELEASED_HEADER}\n\n{section}\n"
    new_body = pattern.sub(replacement, body_text, count=1)

    # Update footer: prepend new entry (or replace if a stale one exists).
    new_link = f"[{version}]: https://github.com/{repo}/releases/tag/{tag}"
    footer_lines = [
        ln for ln in footer_lines if not ln.startswith(f"[{version}]:")
    ]
    footer_lines.insert(0, new_link)

    new_footer = "\n".join(footer_lines) + "\n"
    return new_body.rstrip() + "\n\n" + new_footer


def sync(
    *,
    version: str,
    date: str,
    body: str,
    existing: str,
    repo: str,
    tag: str,
    idempotent: bool,
) -> SyncResult:
    cleaned_version = normalise_version(version)
    cleaned_date = parse_date(date)

    if idempotent and re.search(
        rf"^##\s+\[{re.escape(cleaned_version)}\]",
        existing,
        flags=re.MULTILINE,
    ):
        return SyncResult(status="noop", new_changelog=existing, section="")

    cleaned_body = clean_release_body(body)
    section = build_section(cleaned_version, cleaned_date, cleaned_body)
    new_changelog = insert_section(
        existing=existing,
        section=section,
        version=cleaned_version,
        repo=repo,
        tag=tag,
    )

    if new_changelog == existing:
        return SyncResult(status="noop", new_changelog=existing, section=section)

    return SyncResult(
        status="written",
        new_changelog=new_changelog,
        section=section,
    )


def _read_body(args: argparse.Namespace) -> str:
    if args.body_file:
        path = pathlib.Path(args.body_file)
        if path.exists():
            return path.read_text(encoding="utf-8")
    env = os.environ.get("BODY")
    if env is not None:
        return env
    raise SystemExit(
        "no release body: provide --body-file PATH or set BODY env var",
    )


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="release tag (e.g. v2.1.0)")
    parser.add_argument(
        "--date",
        required=True,
        help="release.published_at (ISO8601) or YYYY-MM-DD",
    )
    parser.add_argument(
        "--changelog",
        default="CHANGELOG.md",
        help="path to CHANGELOG.md (default: CHANGELOG.md)",
    )
    parser.add_argument(
        "--body-file",
        default=None,
        help="path to release body text (otherwise read from BODY env var)",
    )
    parser.add_argument(
        "--repo",
        default=GITHUB_REPO_DEFAULT,
        help=f"owner/name of the repo (default: {GITHUB_REPO_DEFAULT})",
    )
    parser.add_argument(
        "--idempotent",
        action="store_true",
        help="exit noop if version section already present",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="print result to stdout instead of writing CHANGELOG.md",
    )
    args = parser.parse_args(argv)

    body = _read_body(args)
    existing_path = pathlib.Path(args.changelog)
    if not existing_path.exists():
        print(f"error: {args.changelog} not found", file=sys.stderr)
        return 1
    existing = existing_path.read_text(encoding="utf-8")

    try:
        result = sync(
            version=args.version,
            date=args.date,
            body=body,
            existing=existing,
            repo=args.repo,
            tag=args.version,
            idempotent=args.idempotent,
        )
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    if result.status == "noop":
        print(f"status=noop version={args.version}")
        if args.dry_run:
            sys.stdout.write(result.new_changelog)
        return 0

    if args.dry_run:
        sys.stdout.write(result.new_changelog)
        print(
            f"\n# status=written version={args.version} (dry-run, no file changed)",
            file=sys.stderr,
        )
        return 0

    existing_path.write_text(result.new_changelog, encoding="utf-8")
    print(f"status=written version={args.version} path={args.changelog}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
