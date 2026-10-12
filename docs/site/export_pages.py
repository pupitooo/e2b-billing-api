"""Build the selected guides and a standalone Scalar reference for GitHub Pages."""

import argparse
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.parse import urlsplit

import yaml

from hooks import hosted_markdown

SOURCE_DIRECTORY = Path("/source")
SCALAR_BUNDLE = Path("/opt/scalar/scalar.js")
LOCAL_TOOL_ROUTES = ("/demo/", "/database/")
LOCAL_API_URL = "http://127.0.0.1:8081"
LOCAL_PORTAL_URL = "http://127.0.0.1:8082"


def hosted_navigation(entries, site_url):
    """Keep guide navigation and prefix the reference route, omitting live tools."""
    result = []
    base_path = urlsplit(site_url).path.rstrip("/")
    for entry in entries:
        item = {}
        for label, target in entry.items():
            if isinstance(target, list):
                item[label] = hosted_navigation(target, site_url)
            elif target not in LOCAL_TOOL_ROUTES:
                item[label] = base_path + target if target.startswith("/") else target
        if item:
            result.append(item)
    return result


def hosted_specification(value, site_url):
    """Preserve the API contract while adapting documentation links and local servers."""
    if isinstance(value, list):
        return [hosted_specification(item, site_url) for item in value]
    if not isinstance(value, dict):
        return value

    result = {}
    for key, item in value.items():
        if key == "description" and isinstance(item, str):
            result[key] = hosted_markdown(item, site_url)
        elif key == "servers":
            result[key] = []
            for server in item:
                server = dict(server)
                if server["url"] == "/api":
                    server.update(url=LOCAL_API_URL, description="Local API; GitHub Pages hosts documentation only")
                elif server["url"] == "/":
                    server.update(url=LOCAL_PORTAL_URL, description="Local docs portal; unavailable on GitHub Pages")
                result[key].append(server)
        else:
            result[key] = hosted_specification(item, site_url)
    return result


def export_pages(source, destination, site_url):
    """Render an isolated configuration, then add self-contained reference assets."""
    configuration = yaml.safe_load((source / "site/mkdocs.yml").read_text())
    configuration.update(docs_dir=str(source), site_dir=str(destination), site_url=site_url)
    configuration["hooks"] = [str(source / "site/hooks.py")]
    configuration["extra"]["publication_mode"] = "pages"
    configuration["nav"] = hosted_navigation(configuration["nav"], site_url)

    with tempfile.TemporaryDirectory() as temporary:
        configuration_file = Path(temporary) / "mkdocs.yml"
        configuration_file.write_text(yaml.safe_dump(configuration, sort_keys=False))
        subprocess.run(
            ["mkdocs", "build", "--strict", "--config-file", str(configuration_file)], check=True,
        )

    reference = destination / "reference"
    reference.mkdir()
    shutil.copyfile(source / "site/reference.html", reference / "index.html")
    shutil.copyfile(SCALAR_BUNDLE, reference / "scalar.js")

    specification = yaml.safe_load((source / "api/openapi.yaml").read_text())
    specification = yaml.safe_dump(hosted_specification(specification, site_url), sort_keys=False)
    (reference / "openapi.yaml").write_text(specification)
    (destination / "api/openapi.yaml").write_text(specification)

    # Preserve the diagram route used by the local reference's OpenAPI introduction.
    diagram = reference / "diagrams/api-endpoints"
    diagram.mkdir(parents=True)
    for extension in ("png", "mmd"):
        filename = f"api-endpoints.{extension}"
        shutil.copyfile(source / "diagrams/api-endpoints" / filename, diagram / filename)


def site_address(value):
    """Require an HTTP site address with no query or fragment for stable asset links."""
    parsed = urlsplit(value)
    if parsed.scheme not in ("http", "https") or not parsed.netloc or "?" in value or "#" in value:
        raise argparse.ArgumentTypeError("site URL must be an HTTP(S) address without query or fragment")
    return value.rstrip("/") + "/"


def main():
    """Accept an explicit publication address and output directory from Make or CI."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site-url", type=site_address, required=True)
    parser.add_argument("--output", type=Path, required=True)
    arguments = parser.parse_args()
    export_pages(SOURCE_DIRECTORY, arguments.output.resolve(), arguments.site_url)


if __name__ == "__main__":
    main()
