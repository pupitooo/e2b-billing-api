"""Select public documentation and preserve repository links during rendering."""

import posixpath
import re
from pathlib import Path
from urllib.parse import urlsplit

from mkdocs.exceptions import PluginError
from pathspec import PathSpec

REPOSITORY_ROOT = "https://github.com/pupitooo/e2b-billing-api/blob/main/"
MARKDOWN_LINK = re.compile(r"(?<=\]\()([^\s)]+)(?=\))")


def navigation_documents(entries):
    """Collect local page paths from nested navigation without allowing URL entries."""
    for entry in entries:
        if isinstance(entry, dict):
            yield from navigation_documents(entry.values())
        elif isinstance(entry, list):
            yield from navigation_documents(entry)
        elif isinstance(entry, str) and not entry.startswith("/") and not urlsplit(entry).scheme:
            yield entry


def on_config(config):
    """Exclude all unselected input before discovery checks for README/index clashes."""
    published = set(navigation_documents(config["nav"]))
    published.update(config["extra"]["published_assets"])
    source = Path(config["docs_dir"])
    private_markdown = [
        "/" + path.relative_to(source).as_posix()
        for path in source.rglob("*.md")
        if path.relative_to(source).as_posix() not in published
    ]
    config["exclude_docs"] = PathSpec.from_lines("gitignore", private_markdown)
    return config


def on_files(files, config):
    """Exclude unselected local files and fail when a selected source is missing."""
    published = set(navigation_documents(config["nav"]))
    published.update(config["extra"]["published_assets"])
    sources = {file.src_uri for file in files if file.src_dir == config["docs_dir"]}
    missing = published - sources
    if missing:
        raise PluginError("Missing published documentation: " + ", ".join(sorted(missing)))

    for file in list(files):
        # Theme/search assets are generated, rather than read from docs_dir.
        if file.src_dir == config["docs_dir"] and file.src_uri not in published:
            files.remove(file)
    return files


def on_page_markdown(markdown, page, **kwargs):
    """Render links outside docs as GitHub links without publishing source files."""
    def repository_link(match):
        target = match.group(0)
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or not parsed.path or target.startswith("/"):
            return target

        resolved = posixpath.normpath(posixpath.join(posixpath.dirname(page.file.src_uri), parsed.path))
        if resolved.startswith("../"):
            repository_path = posixpath.normpath(posixpath.join("docs", resolved))
            return REPOSITORY_ROOT + repository_path + ("#" + parsed.fragment if parsed.fragment else "")
        return target

    return MARKDOWN_LINK.sub(repository_link, markdown)
