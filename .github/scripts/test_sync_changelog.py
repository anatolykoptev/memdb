"""Smoke tests for sync-changelog.py.

Run with::

    python -m unittest .github/scripts/test_sync_changelog.py
"""

from __future__ import annotations

import importlib.util
import pathlib
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
SCRIPT = HERE / "sync-changelog.py"

# Load the script as a module despite the dash in its filename.
_spec = importlib.util.spec_from_file_location("sync_changelog", SCRIPT)
sync_mod = importlib.util.module_from_spec(_spec)
sys.modules["sync_changelog"] = sync_mod
_spec.loader.exec_module(sync_mod)


SAMPLE_BODY_V21 = (HERE / "testdata_v2.1.0_body.md").read_text(encoding="utf-8")

MINIMAL_CHANGELOG = """\
# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [2.0.0] — 2026-04-24

### Added

- Phase D shipped.

[Unreleased]: https://github.com/anatolykoptev/memdb/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/anatolykoptev/memdb/releases/tag/v2.0.0
"""


class CleanReleaseBodyTests(unittest.TestCase):
    def test_strips_whats_changed_block(self) -> None:
        cleaned = sync_mod.clean_release_body(SAMPLE_BODY_V21)
        self.assertNotIn("## What's Changed", cleaned)
        self.assertNotIn("### What's Changed", cleaned)
        # The orphan top-level entry under "What's Changed" is dropped too;
        # the categorised sections remain.
        self.assertNotIn(
            "M4: expose Phase D hyperparams as runtime env vars (#55)",
            cleaned,
        )

    def test_strips_full_changelog_footer(self) -> None:
        cleaned = sync_mod.clean_release_body(SAMPLE_BODY_V21)
        self.assertNotIn("**Full Changelog:**", cleaned)

    def test_demotes_category_headers(self) -> None:
        cleaned = sync_mod.clean_release_body(SAMPLE_BODY_V21)
        # release-drafter emits "## Features"; we want "### Features".
        self.assertIn("### Features", cleaned)
        self.assertIn("### Bug Fixes", cleaned)
        self.assertIn("### Documentation", cleaned)
        # No level-2 headers should survive in the cleaned body.
        for line in cleaned.splitlines():
            self.assertFalse(
                line.startswith("## ") and not line.startswith("### "),
                msg=f"unexpected level-2 header survived: {line!r}",
            )

    def test_collapses_blank_runs(self) -> None:
        weird = "### A\n\n\n\n- one\n\n\n### B\n\n- two\n"
        cleaned = sync_mod.clean_release_body(weird)
        self.assertNotIn("\n\n\n", cleaned)


class InsertSectionTests(unittest.TestCase):
    def test_inserts_after_unreleased_and_updates_footer(self) -> None:
        result = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24T06:53:18Z",
            body=SAMPLE_BODY_V21,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        self.assertEqual(result.status, "written")
        out = result.new_changelog

        # New section heading is present with the correct date (em-dash).
        self.assertIn("## [2.1.0] — 2026-04-24", out)

        # Old [Unreleased] heading is preserved (so future syncs work).
        self.assertIn("## [Unreleased]", out)

        # Old version is still there.
        self.assertIn("## [2.0.0] — 2026-04-24", out)

        # Footer link present for v2.1.0; v2.0.0 link retained.
        self.assertIn(
            "[2.1.0]: https://github.com/anatolykoptev/memdb/releases/tag/v2.1.0",
            out,
        )
        self.assertIn(
            "[2.0.0]: https://github.com/anatolykoptev/memdb/releases/tag/v2.0.0",
            out,
        )
        # [Unreleased] link is present and refreshed to compare against the
        # new tag.
        self.assertIn(
            "[Unreleased]: https://github.com/anatolykoptev/memdb/compare/v2.1.0...HEAD",
            out,
        )

        # Ordering: 2.1.0 heading must precede 2.0.0 heading.
        self.assertLess(
            out.index("## [2.1.0]"),
            out.index("## [2.0.0]"),
        )

        # Footer link ordering: [Unreleased] -> [2.1.0] -> [2.0.0].
        self.assertLess(
            out.index("[Unreleased]:"),
            out.index("[2.1.0]:"),
        )
        self.assertLess(
            out.index("[2.1.0]:"),
            out.index("[2.0.0]:"),
        )

    def test_unreleased_link_stays_on_top(self) -> None:
        result = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        out = result.new_changelog
        # Pull just the trailing footer block for ordering assertions.
        footer_lines = [
            ln
            for ln in out.splitlines()
            if ln.startswith("[") and "]: " in ln
        ]
        self.assertGreaterEqual(len(footer_lines), 3)
        self.assertTrue(footer_lines[0].startswith("[Unreleased]:"))
        self.assertTrue(footer_lines[1].startswith("[2.1.0]:"))
        # Whatever was previously below must still be below the new entry.
        self.assertTrue(footer_lines[2].startswith("[2.0.0]:"))

    def test_unreleased_link_url_refreshed(self) -> None:
        result = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        out = result.new_changelog
        # Old compare URL (against v2.0.0) is gone; new one targets v2.1.0.
        self.assertNotIn(
            "[Unreleased]: https://github.com/anatolykoptev/memdb/compare/v2.0.0...HEAD",
            out,
        )
        self.assertIn(
            "[Unreleased]: https://github.com/anatolykoptev/memdb/compare/v2.1.0...HEAD",
            out,
        )

    def test_em_dash_in_section_heading(self) -> None:
        result = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        out = result.new_changelog
        # Em-dash (U+2014) form, not the ASCII hyphen form.
        self.assertIn("## [2.1.0] — 2026-04-24", out)
        self.assertNotIn("## [2.1.0] - 2026-04-24", out)

    def test_default_idempotent_no_flag(self) -> None:
        # Default behaviour: section already present -> noop on second call.
        first = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        second = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=first.new_changelog,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        self.assertEqual(second.status, "noop")
        self.assertEqual(second.new_changelog, first.new_changelog)

    def test_force_flag_inserts_duplicate(self) -> None:
        first = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        second = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=SAMPLE_BODY_V21,
            existing=first.new_changelog,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
            force=True,
        )
        self.assertEqual(second.status, "written")
        # The same heading should now appear twice in the output.
        self.assertEqual(
            second.new_changelog.count("## [2.1.0] — 2026-04-24"),
            2,
        )

    def test_empty_body_emits_placeholder(self) -> None:
        # A body that cleans to empty (only the cruft sections) should still
        # produce a valid section with a placeholder line.
        empty_body = (
            "## What's Changed\n\n"
            "- nothing categorised\n\n"
            "**Full Changelog:** https://example/compare/A...B\n"
        )
        result = sync_mod.sync(
            version="v2.1.0",
            date="2026-04-24",
            body=empty_body,
            existing=MINIMAL_CHANGELOG,
            repo="anatolykoptev/memdb",
            tag="v2.1.0",
        )
        self.assertEqual(result.status, "written")
        self.assertIn("_No notable changes._", result.new_changelog)

    def test_raises_when_unreleased_missing(self) -> None:
        bad = "# Changelog\n\n## [1.0.0] — 2026-01-01\n\n- old\n"
        with self.assertRaises(ValueError):
            sync_mod.sync(
                version="v2.0.0",
                date="2026-04-24",
                body=SAMPLE_BODY_V21,
                existing=bad,
                repo="anatolykoptev/memdb",
                tag="v2.0.0",
                force=True,
            )


class CliDryRunTests(unittest.TestCase):
    def test_dry_run_writes_to_stdout_not_disk(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cl = pathlib.Path(tmp) / "CHANGELOG.md"
            cl.write_text(MINIMAL_CHANGELOG, encoding="utf-8")
            body_file = pathlib.Path(tmp) / "body.txt"
            body_file.write_text(SAMPLE_BODY_V21, encoding="utf-8")

            argv = [
                "--version",
                "v2.1.0",
                "--date",
                "2026-04-24T06:53:18Z",
                "--changelog",
                str(cl),
                "--body-file",
                str(body_file),
                "--dry-run",
            ]
            rc = sync_mod.main(argv)
            self.assertEqual(rc, 0)
            # Disk unchanged in dry-run.
            self.assertEqual(cl.read_text(encoding="utf-8"), MINIMAL_CHANGELOG)


class CliWriteAndIdempotentTests(unittest.TestCase):
    def test_write_then_rerun_is_noop(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cl = pathlib.Path(tmp) / "CHANGELOG.md"
            cl.write_text(MINIMAL_CHANGELOG, encoding="utf-8")
            body_file = pathlib.Path(tmp) / "body.txt"
            body_file.write_text(SAMPLE_BODY_V21, encoding="utf-8")

            argv = [
                "--version",
                "v2.1.0",
                "--date",
                "2026-04-24T06:53:18Z",
                "--changelog",
                str(cl),
                "--body-file",
                str(body_file),
            ]

            self.assertEqual(sync_mod.main(argv), 0)
            after_first = cl.read_text(encoding="utf-8")
            self.assertIn("## [2.1.0] — 2026-04-24", after_first)

            self.assertEqual(sync_mod.main(argv), 0)
            after_second = cl.read_text(encoding="utf-8")
            self.assertEqual(after_first, after_second)


if __name__ == "__main__":
    unittest.main()
