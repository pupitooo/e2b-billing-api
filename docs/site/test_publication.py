"""Verify the real MkDocs build preserves public assets and rejects broken content."""

import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml

SOURCE_DIRECTORY = Path("/source")
CONFIGURATION_FILE = SOURCE_DIRECTORY / "site/mkdocs.yml"


# TestBuild verifies complete isolated builds, including private files and link failures.
class TestBuild(unittest.TestCase):
    # test_build declares each input change and its literal expected build outcome.
    def test_build(self):
        cases = [
            {
                "scenario": "public_assets_survive_and_private_files_are_excluded",
                "input_files": {
                    "guides/private-probe.md": "# PRIVATE_PUBLICATION_PROBE\n",
                    "guides/private-probe.json": '{"secret":"PRIVATE_PUBLICATION_PROBE"}\n',
                    "README.md": "# PRIVATE_PUBLICATION_PROBE\n",
                    "diagrams/README.md": "# PRIVATE_PUBLICATION_PROBE\n",
                },
                "input_removed_file": None,
                "want_exit_code": 0,
                "want_error": None,
                "want_published_files": [
                    "index.html", "diagrams/index.html",
                    "guides/accounting-recovery/index.html",
                    "simulator/custom-scenario.json", "api/openapi.yaml",
                    "diagrams/option-c-components/option-c-components.png",
                ],
                "want_excluded_files": [
                    "guides/private-probe/index.html", "guides/private-probe.json",
                    "README.md", "diagrams/README.md",
                ],
                "want_asset_patterns": ["assets/stylesheets/*.css", "assets/javascripts/*.js"],
                "want_absent_search_text": "PRIVATE_PUBLICATION_PROBE",
            },
            {
                "scenario": "missing_selected_document_fails",
                "input_files": {},
                "input_removed_file": "guides/accounting-recovery.md",
                "want_exit_code": 1,
                "want_error": "Missing published documentation: guides/accounting-recovery.md",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
            },
            {
                "scenario": "broken_document_link_fails",
                "input_files": {"index.md": "# Home\n\n[Missing](missing.md)\n"},
                "input_removed_file": None,
                "want_exit_code": 1,
                "want_error": "'missing.md'",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
            },
            {
                "scenario": "broken_document_anchor_fails",
                "input_files": {"index.md": "# Home\n\n[Missing](guides/testing.md#absent-probe-anchor)\n"},
                "input_removed_file": None,
                "want_exit_code": 1,
                "want_error": "#absent-probe-anchor",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
            },
        ]

        for case in cases:
            with self.subTest(scenario=case["scenario"]), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                source = directory / "source"
                site = directory / "site"
                shutil.copytree(SOURCE_DIRECTORY, source)
                for relative_path, content in case["input_files"].items():
                    (source / relative_path).write_text(content)
                if case["input_removed_file"]:
                    (source / case["input_removed_file"]).unlink()

                configuration = yaml.safe_load(CONFIGURATION_FILE.read_text())
                configuration.update(docs_dir=str(source), site_dir=str(site))
                configuration["hooks"] = [str(SOURCE_DIRECTORY / "site/hooks.py")]
                configuration_file = directory / "mkdocs.yml"
                configuration_file.write_text(yaml.safe_dump(configuration, sort_keys=False))

                result = subprocess.run(
                    ["mkdocs", "build", "--strict", "--config-file", str(configuration_file)],
                    text=True, capture_output=True, check=False,
                )

                self.assertEqual(result.returncode, case["want_exit_code"],
                                 f"build inputs={case['input_files']} removed={case['input_removed_file']}: "
                                 f"exit={result.returncode}, want={case['want_exit_code']}\n{result.stderr}")
                if case["want_error"] is not None:
                    self.assertIn(case["want_error"], result.stderr)
                    continue

                for relative_path in case["want_published_files"]:
                    self.assertTrue((site / relative_path).is_file(), f"build missing public file {relative_path}")
                for relative_path in case["want_excluded_files"]:
                    self.assertFalse((site / relative_path).exists(), f"build exposed private file {relative_path}")

                for pattern in case["want_asset_patterns"]:
                    self.assertTrue(list(site.glob(pattern)), f"build removed theme assets matching {pattern}")
                search = json.loads((site / "search/search_index.json").read_text())
                self.assertNotIn(case["want_absent_search_text"], json.dumps(search))


if __name__ == "__main__":
    unittest.main(verbosity=2)
