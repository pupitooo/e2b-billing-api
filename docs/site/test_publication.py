"""Verify the real MkDocs build preserves public assets and rejects broken content."""

import argparse
import json
from html.parser import HTMLParser
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from urllib.parse import unquote, urlsplit

import yaml
import markdown

from runtime import write_runtime
from export_pages import export_pages, site_address

SOURCE_DIRECTORY = Path("/source")
CONFIGURATION_FILE = SOURCE_DIRECTORY / "site/mkdocs.yml"


# ResourceLinks collects browser-visible resources without interpreting JavaScript.
class ResourceLinks(HTMLParser):
    # __init__ prepares an independent link list for each rendered page.
    def __init__(self):
        super().__init__()
        self.links = []

    # handle_starttag captures navigation, image and script paths for file checks.
    def handle_starttag(self, tag, attrs):
        for name, value in attrs:
            if name in ("href", "src") and value:
                self.links.append(value)


# TestExportPages verifies the full static deliverable at a root and project subpath.
class TestExportPages(unittest.TestCase):
    # test_export_pages checks isolated exports, repeat cleanup, API links and exclusions.
    def test_export_pages(self):
        cases = [
            {
                "scenario": "project_site_under_repository_path",
                "input_site_url": "https://pupitooo.github.io/e2b-billing-api/",
                "want_base_path": "/e2b-billing-api",
                "want_reference_link": 'href="/e2b-billing-api/reference/"',
                "want_specification_link": 'href="/e2b-billing-api/reference/openapi.yaml"',
                "want_description_link": "[API guide](/e2b-billing-api/guides/api-reference/)",
            },
            {
                "scenario": "site_at_domain_root",
                "input_site_url": "https://docs.example.test/",
                "want_base_path": "",
                "want_reference_link": 'href="/reference/"',
                "want_specification_link": 'href="/reference/openapi.yaml"',
                "want_description_link": "[API guide](/guides/api-reference/)",
            },
        ]
        expectations = {
            "input_private_files": {
                "guides/private-probe.md": "# PRIVATE_PAGES_PROBE\n",
                "private-probe.json": '{"secret":"PRIVATE_PAGES_PROBE"}\n',
            },
            "input_stale_output": {"demo/index.html": "STALE_DEMO_PROBE"},
            "want_published_files": [
                "index.html", "search/search_index.json", "reference/index.html",
                "reference/scalar.js", "reference/openapi.yaml", "api/openapi.yaml",
                "reference/diagrams/api-endpoints/api-endpoints.png",
                "reference/diagrams/api-endpoints/api-endpoints.mmd",
                "diagrams/option-c-components/option-c-components.png",
                "diagrams/option-c-components/option-c-components.mmd",
            ],
            "want_excluded_files": [
                "demo", "database", "guides/private-probe", "private-probe.json", "README.md",
            ],
            "want_absent_search_text": "PRIVATE_PAGES_PROBE",
            "want_local_tools_text": "Demo console (local only)",
            "want_reference_options": ["layout: 'modern'", "hideClientButton: true", "hideTestRequestButton: true"],
            "want_api_server": {"url": "http://127.0.0.1:8081", "description": "Local API; GitHub Pages hosts documentation only"},
            "want_portal_server": {"url": "http://127.0.0.1:8082", "description": "Local docs portal; unavailable on GitHub Pages"},
        }
        for case in cases:
            with self.subTest(scenario=case["scenario"]), tempfile.TemporaryDirectory() as temporary:
                source = Path(temporary) / "source"
                destination = Path(temporary) / "site"
                shutil.copytree(SOURCE_DIRECTORY, source)
                for path, content in expectations["input_private_files"].items():
                    (source / path).write_text(content)
                for path, content in expectations["input_stale_output"].items():
                    (destination / path).parent.mkdir(parents=True, exist_ok=True)
                    (destination / path).write_text(content)

                export_pages(source, destination, case["input_site_url"])

                for path in expectations["want_published_files"]:
                    self.assertTrue((destination / path).is_file(), f"export URL={case['input_site_url']}: missing {path}")
                for path in expectations["want_excluded_files"]:
                    self.assertFalse((destination / path).exists(), f"export URL={case['input_site_url']}: exposed {path}")

                home = (destination / "index.html").read_text()
                self.assertIn(case["want_reference_link"], home)
                self.assertIn(case["want_specification_link"], home)
                self.assertIn(expectations["want_local_tools_text"], home)
                search = (destination / "search/search_index.json").read_text()
                self.assertNotIn(expectations["want_absent_search_text"], search)
                reference = (destination / "reference/index.html").read_text()
                for option in expectations["want_reference_options"]:
                    self.assertIn(option, reference)

                specification = yaml.safe_load((destination / "reference/openapi.yaml").read_text())
                original = yaml.safe_load((source / "api/openapi.yaml").read_text())
                self.assertEqual(specification["openapi"], "3.1.2")
                self.assertEqual(specification["components"], original["components"])
                self.assertEqual(set(specification["paths"]), set(original["paths"]))
                self.assertEqual(specification["servers"], [expectations["want_api_server"]])
                self.assertEqual(specification["paths"]["/demo/runtime.json"]["servers"], [expectations["want_portal_server"]])
                self.assertIn(case["want_description_link"], specification["info"]["description"])
                self.assertEqual((destination / "api/openapi.yaml").read_text(), (destination / "reference/openapi.yaml").read_text())

                self.check_resources(destination, case["want_base_path"], specification)

    # check_resources checks all HTML pages and documentation links in the API contract.
    def check_resources(self, destination, base_path, specification):
        for page in destination.rglob("*.html"):
            self.check_html_links(destination, page, page.read_text(), base_path)
        self.check_description_links(destination, specification, base_path)

    # check_description_links visits nested endpoint descriptions without changing schemas.
    def check_description_links(self, destination, value, base_path):
        if isinstance(value, list):
            for item in value:
                self.check_description_links(destination, item, base_path)
        elif isinstance(value, dict):
            for key, item in value.items():
                if key == "description" and isinstance(item, str):
                    self.check_html_links(destination, destination / "reference/index.html", markdown.markdown(item), base_path)
                else:
                    self.check_description_links(destination, item, base_path)

    # check_html_links resolves browser-visible paths and rejects missing files or base escapes.
    def check_html_links(self, destination, page, html, base_path):
        parser = ResourceLinks()
        parser.feed(html)
        for link in parser.links:
            target = urlsplit(link)
            if target.scheme or target.netloc or not target.path:
                continue
            path = unquote(target.path)
            if path.startswith("/"):
                self.assertTrue(path.startswith(base_path + "/"), f"{page}: link escapes site base: {link}")
                resource = destination / path[len(base_path):].lstrip("/")
            else:
                resource = page.parent / path
            if resource.is_dir():
                resource /= "index.html"
            self.assertTrue(resource.is_file(), f"{page.relative_to(destination)}: missing linked resource {link}")

    # test_site_address checks invalid inputs and adjacent valid URL forms before building.
    def test_site_address(self):
        cases = [
            {"scenario": "HTTPS project", "input": "https://example.test/project", "want": "https://example.test/project/", "want_error": None},
            {"scenario": "HTTP preview", "input": "http://127.0.0.1:8083/", "want": "http://127.0.0.1:8083/", "want_error": None},
            {"scenario": "domain root without slash", "input": "https://example.test", "want": "https://example.test/", "want_error": None},
            {"scenario": "missing scheme", "input": "example.test/project", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
            {"scenario": "missing host", "input": "https:///project", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
            {"scenario": "query", "input": "https://example.test/?mode=test", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
            {"scenario": "empty query", "input": "https://example.test/?", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
            {"scenario": "fragment", "input": "https://example.test/#guide", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
            {"scenario": "empty fragment", "input": "https://example.test/#", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
            {"scenario": "unsupported scheme", "input": "ftp://example.test/", "want": None, "want_error": "site URL must be an HTTP(S) address without query or fragment"},
        ]
        for case in cases:
            with self.subTest(scenario=case["scenario"]):
                if case["want_error"]:
                    with self.assertRaises(argparse.ArgumentTypeError) as error:
                        site_address(case["input"])
                    self.assertEqual(str(error.exception), case["want_error"])
                else:
                    self.assertEqual(site_address(case["input"]), case["want"])


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
                "input_navigation_entry": None,
                "want_exit_code": 0,
                "want_error": None,
                "want_published_files": [
                    "index.html", "diagrams/index.html",
                    "guides/accounting-recovery/index.html", "guides/api-reference/index.html",
                    "guides/demo-console/index.html",
                    "simulator/custom-scenario.json", "api/openapi.yaml",
                    "diagrams/option-c-components/option-c-components.png",
                    "diagrams/api-endpoints/api-endpoints.png",
                    "diagrams/api-endpoints/api-endpoints.mmd",
                ],
                "want_excluded_files": [
                    "guides/private-probe/index.html", "guides/private-probe.json",
                    "README.md", "diagrams/README.md",
                ],
                "want_asset_patterns": ["assets/stylesheets/*.css", "assets/javascripts/*.js"],
                "want_absent_search_text": "PRIVATE_PUBLICATION_PROBE",
                "want_html_links": {
                    "index.html": 'href="/reference/openapi.yaml"',
                    "guides/api-reference/index.html": 'src="../../diagrams/api-endpoints/api-endpoints.png"',
                },
            },
            {
                "scenario": "missing_selected_document_fails",
                "input_files": {},
                "input_removed_file": "guides/accounting-recovery.md",
                "input_navigation_entry": None,
                "want_exit_code": 1,
                "want_error": "Missing published documentation: guides/accounting-recovery.md",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
            {
                "scenario": "navigation_under_the_API_proxy_fails",
                "input_files": {"api/index.md": "# API guide\n"},
                "input_removed_file": None,
                "input_navigation_entry": {"Reserved API route": "api/index.md"},
                "want_exit_code": 1,
                "want_error": "Documentation page 'api/index.md' uses reserved route 'api/'",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
            {
                "scenario": "navigation_under_the_Scalar_proxy_fails",
                "input_files": {"reference/index.md": "# Reference guide\n"},
                "input_removed_file": None,
                "input_navigation_entry": {"Reserved Scalar route": "reference/index.md"},
                "want_exit_code": 1,
                "want_error": "Documentation page 'reference/index.md' uses reserved route 'reference/'",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
            {
                "scenario": "navigation_under_the_demo_route_fails",
                "input_files": {"demo/index.md": "# Reserved guide\n"},
                "input_removed_file": None,
                "input_navigation_entry": {"Reserved route": "demo/index.md"},
                "want_exit_code": 1,
                "want_error": "Documentation page 'demo/index.md' uses reserved route 'demo/'",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
            {
                "scenario": "navigation_under_the_database_route_fails",
                "input_files": {"database/index.md": "# Reserved guide\n"},
                "input_removed_file": None,
                "input_navigation_entry": {"Reserved route": "database/index.md"},
                "want_exit_code": 1,
                "want_error": "Documentation page 'database/index.md' uses reserved route 'database/'",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
            {
                "scenario": "broken_document_link_fails",
                "input_files": {"index.md": "# Home\n\n[Missing](missing.md)\n"},
                "input_removed_file": None,
                "input_navigation_entry": None,
                "want_exit_code": 1,
                "want_error": "'missing.md'",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
            {
                "scenario": "broken_document_anchor_fails",
                "input_files": {"index.md": "# Home\n\n[Missing](guides/testing.md#absent-probe-anchor)\n"},
                "input_removed_file": None,
                "input_navigation_entry": None,
                "want_exit_code": 1,
                "want_error": "#absent-probe-anchor",
                "want_published_files": [], "want_excluded_files": [],
                "want_asset_patterns": [], "want_absent_search_text": None,
                "want_html_links": {},
            },
        ]

        for case in cases:
            with self.subTest(scenario=case["scenario"]), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                source = directory / "source"
                site = directory / "site"
                shutil.copytree(SOURCE_DIRECTORY, source)
                for relative_path, content in case["input_files"].items():
                    (source / relative_path).parent.mkdir(parents=True, exist_ok=True)
                    (source / relative_path).write_text(content)
                if case["input_removed_file"]:
                    (source / case["input_removed_file"]).unlink()

                configuration = yaml.safe_load(CONFIGURATION_FILE.read_text())
                configuration.update(docs_dir=str(source), site_dir=str(site))
                if case["input_navigation_entry"]:
                    configuration["nav"].append(case["input_navigation_entry"])
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
                for relative_path, link in case["want_html_links"].items():
                    self.assertIn(link, (site / relative_path).read_text(),
                                  f"rendered {relative_path} missing expected resource link {link}")
                search = json.loads((site / "search/search_index.json").read_text())
                self.assertNotIn(case["want_absent_search_text"], json.dumps(search))


# TestRuntimeMetadata checks isolated startup files without exposing other environment values.
class TestRuntimeMetadata(unittest.TestCase):
    # test_write_runtime keeps the real mode minimal and safely encodes the configured fixed value.
    def test_write_runtime(self):
        cases = [
            {"name": "real mode", "fixed_now": "", "want": {"clock_mode": "real"}},
            {"name": "fixed UTC clock", "fixed_now": "2026-11-01T00:00:00Z", "want": {"clock_mode": "fixed", "fixed_now": "2026-11-01T00:00:00Z"}},
            {"name": "metadata preserves offset and precision", "fixed_now": "2026-11-01T08:00:00.123456+08:00", "want": {"clock_mode": "fixed", "fixed_now": "2026-11-01T08:00:00.123456+08:00"}},
        ]
        for case in cases:
            with self.subTest(case["name"]), tempfile.TemporaryDirectory() as directory:
                destination = Path(directory) / "runtime.json"
                write_runtime(destination, case["fixed_now"])
                self.assertEqual(json.loads(destination.read_text()), case["want"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
