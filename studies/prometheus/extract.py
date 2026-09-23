#!/usr/bin/env python3
"""Reconstruct the pinned documentation menus; never run the website's fetcher.

Python 3 standard library only. All source Git operations are read-only. By
default, check the shipped fixtures; --tree prints regenerated Markdown.
--write explicitly regenerates only the two sibling Markdown tree files.
"""

import argparse
import collections
import difflib
import itertools
import json
import os
from pathlib import Path
import re
import subprocess
import sys


HERE = Path(__file__).resolve().parent
REVISIONS = {
    "current": "1fed0cfef668ede0f52461305d4a52e44f199562",
    "candidate": "029b80a42ad22868dbfed338019cafd270fbfbda",
}
REPOS = {
    "prometheus": ("3.14", "d7598b7141418fa35be2b5ec5d0fefb634199610", "prometheus"),
    "alertmanager": ("0.34", "085f0ef7eb41da24cab8cd000f1345b6250f2edb", "alerting"),
}
ID = r"[a-z][a-z0-9-]{0,95}"
LINE = re.compile(rf"^( *)- \[([^\[\]<>]+)\]\((group|page):({ID})(?:/({ID}))?\)$")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def git(repo, *args):
    # Disallow lazy fetching from partial clones: missing objects must fail,
    # rather than causing an implicit write to the read-only source repository.
    env = dict(os.environ, GIT_OPTIONAL_LOCKS="0", GIT_NO_LAZY_FETCH="1")
    return subprocess.check_output(
        ["git", "-C", str(repo), *args], text=True, env=env
    )


def documents(repo, revision):
    """Match findFiles' sorted, recursive directory-entry discovery order."""
    paths = git(repo, "ls-tree", "-r", "--name-only", revision, "docs").splitlines()
    entries = {}
    for path in paths:
        node = entries
        parts = path.split("/")
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        node[parts[-1]] = path

    def walk(node):
        for name in sorted(node):
            value = node[name]
            if isinstance(value, dict):
                yield from walk(value)
            elif value.endswith(".md"):
                yield value

    for path in walk(entries):
        yield path.removeprefix("docs/"), git(repo, "show", f"{revision}:{path}")


def frontmatter(text):
    """Read only the scalar navigation fields present at these pinned revisions.

    This deliberately is not a general YAML parser. Unknown complex metadata
    (e.g. specification authors) is irrelevant; complex navigation fields fail.
    """
    require(text.startswith("---\n"), "Missing frontmatter")
    data = {}
    for line in text.split("---", 2)[1].splitlines():
        match = re.fullmatch(r"(title|nav_title|sort_rank|hide_in_nav):\s*(.*)", line)
        if not match:
            continue
        key, value = match.groups()
        if key == "sort_rank":
            require(bool(re.fullmatch(r"-?\d+", value)), "Complex sort_rank")
            value = int(value)
        elif key == "hide_in_nav":
            require(value in ("true", "false"), "Complex hide_in_nav")
            value = value == "true"
        elif value.startswith('"'):
            value = json.loads(value)
        elif value.startswith("'"):
            require(value.endswith("'"), "Invalid quoted title")
            value = value[1:-1].replace("''", "'")
        else:
            require(value and value not in ("|", ">"), "Complex navigation title")
        data[key] = value
    require("title" in data, "Missing title")
    return data


def route_table(block):
    result = {}
    for line in block.splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r'\s*"([^"]+)": \{ (.+) \},', line)
        require(match is not None, f"Unsupported route syntax: {line}")
        path, fields = match.groups()
        route = {}
        for field in fields.split(", "):
            name, raw = field.split(": ", 1)
            require(name in ("slug", "navTitle", "sortRank", "redirectTo"), name)
            route[name] = json.loads(raw)
        require(path not in result, f"Duplicate source route: {path}")
        result[path] = route
    return result


def routes(repo):
    text = git(repo, "show", f"{REVISIONS['candidate']}:docs-routes.ts")
    local = text.split("export const localDocRoutes:", 1)[1].split("= {\n", 1)[1].split("\n};", 1)[0]
    external = text.split("export const githubDocRoutes:", 1)[1].split("= {\n", 1)[1].split("\n};", 1)[0]
    result = {"site": route_table(local)}
    for repo_name in REPOS:
        block = external.split(f"  {repo_name}: {{\n", 1)[1].split("\n  },", 1)[0]
        result[repo_name] = route_table(block)
    return result


def content_id(source, path):
    return source + "-" + re.sub(r"[^a-z0-9]+", "-", path.removesuffix(".md").lower())


def collection(docs_repo, variant):
    routing = routes(docs_repo) if variant == "candidate" else None
    docs = {}
    sources = []
    # fetch-repo-docs.ts inserts repository docs first, in docs-config order.
    for name, (version, commit, prefix) in REPOS.items():
        repo = docs_repo / "generated/repo-docs/prometheus" / name / version
        require(git(repo, "rev-parse", "HEAD").strip() == commit, f"Unexpected cached {name} revision")
        sources.append((name, repo, commit, prefix + "/latest"))
    sources.append(("site", docs_repo, REVISIONS[variant], ""))

    for source, repo, revision, prefix in sources:
        for path, text in documents(repo, revision):
            if source != "site" and path == "index.md":
                continue  # Explicit exclusion in the website fetch script.
            route = routing[source][path] if routing is not None else {}
            if "redirectTo" in route:
                continue  # Redirects do not enter docsCollection or the nav.
            meta = frontmatter(text)
            default_slug = re.sub(r"(/index)*\.md$", "", path)
            slug = route.get("slug", "/".join(filter(None, (prefix, default_slug))))
            slug = slug.replace(":version", "latest")
            require(slug not in docs, f"Duplicate effective route: {slug}")
            docs[slug] = {
                "slug": slug,
                "source": source,
                "path": path,
                "content_id": content_id(source, path),
                "label": route.get("navTitle", meta.get("nav_title", meta["title"])),
                "rank": route.get("sortRank", meta.get("sort_rank", 0)),
                "hidden": meta.get("hide_in_nav", False),
                "children": [],
            }
    return docs


def hierarchy(docs):
    # JS sort is stable. Parent assignment is in depth order, then discovery
    # order; importantly, it is NOT simply a lexically sorted final menu.
    roots = []
    for slug in sorted(docs, key=lambda key: len(key.split("/"))):
        node = docs[slug]
        parent = slug.rpartition("/")[0]
        while parent and parent not in docs:
            parent = parent.rpartition("/")[0]
        if parent:
            docs[parent]["children"].append(node)
        else:
            roots.append(node)
    # getDocsRoots uses original collection insertion order, not depth order.
    root_slugs = {node["slug"] for node in roots}
    roots = sorted((doc for slug, doc in docs.items() if slug in root_slugs), key=lambda doc: doc["rank"])
    for node in docs.values():
        node["children"].sort(key=lambda child: child["rank"])
    return roots


def render(docs, variant):
    rows = []
    inventory = []

    def walk(nodes, depth):
        for node in nodes:
            occurrence = f"{variant}-{len(inventory) + 1:03d}"
            group = bool(node["children"])
            target = f"group:{occurrence}" if group else f"page:{occurrence}/{node['content_id']}"
            rows.append("  " * depth + f"- [{node['label']}]({target})")
            inventory.append({"occurrence": occurrence, "depth": depth, "selectable": not group,
                              **{k: v for k, v in node.items() if k != "children"}})
            walk([child for child in node["children"] if not child["hidden"]], depth + 1)

    # Product-version aliases were already projected to latest in collection.
    # No hide_in_nav roots exist at the pinned revisions; LeftNav keeps roots.
    walk(hierarchy(docs), 0)
    return "# Documentation\n\n" + "\n".join(rows) + "\n", inventory


def cross_check_cache(docs_repo, candidate):
    """Independent confirmation only; generated metadata never builds a tree."""
    raw = json.loads((docs_repo / "generated/docs-collection.json").read_text())
    cache = {}
    for slug, item in raw.items():
        if item["type"] == "local-doc":
            source, path = "site", item["sourcePath"].removeprefix("docs/")
        elif item.get("routeVersion") == "latest":
            source, path = item["repo"], item["sourcePath"]
            require(item["version"] == REPOS[source][0], "Cached latest version mismatch")
        else:
            continue
        cache[slug] = (source, path, item.get("navTitle", item["title"]), item["sortRank"], item.get("hideInNav", False))
    extracted = {slug: tuple(node[key] for key in ("source", "path", "label", "rank", "hidden"))
                 for slug, node in candidate.items()}
    require(list(cache) == list(extracted), "Cached candidate route/discovery order differs")
    require(cache == extracted, "Cached candidate navigation metadata differs")


def check_config(inventories):
    config = json.loads((HERE / "study.json").read_text())
    require("config" not in config and config["schema_version"] == 1, "Expected Config, not Bundle")
    require(config["title"] == "Finding information in Prometheus documentation", "Study title")
    require(config["tasks_per_session"] == 6, "Expected six tasks/session")
    require(config["variants"] == [
        {"id": "current", "name": "Current navigation", "tree": "current.md"},
        {"id": "candidate", "name": "Proposed navigation", "tree": "candidate.md"},
    ], "Unexpected variants")
    require(all(isinstance(v, str) for v in config["provenance"].values()), "Provenance values must be strings")
    tasks = {task["id"]: task for task in config["tasks"]}
    require(len(tasks) == len(config["tasks"]) == 12, "Expected twelve unique tasks")
    require(collections.Counter(task["difficulty"] for task in tasks.values()) == {"easy": 4, "medium": 4, "hard": 4}, "Difficulty bank balance")
    for variant, inventory in inventories.items():
        contents = {node["content_id"] for node in inventory if node["selectable"]}
        for task in tasks.values():
            answers = task["answers"][variant]
            require(answers and len(answers) == len(set(answers)) and set(answers) <= contents,
                    f"Unreachable/duplicate answers: {task['id']} ({variant})")
            require(set(task["answers"]) == set(REVISIONS), "Answer variants")
            require(task["id"].startswith(task["difficulty"] + "-"), "Task ID band")
    exposure, pairs = collections.Counter(), collections.defaultdict(collections.Counter)
    require(len(config["panels"]) == 6, "Expected six panels")
    for panel in config["panels"]:
        require(len(panel) == len(set(panel)) == 6 and set(panel) <= tasks.keys(), "Invalid panel")
        exposure.update(panel)
        for band in ("easy", "medium", "hard"):
            selected = sorted(task_id for task_id in panel if tasks[task_id]["difficulty"] == band)
            require(len(selected) == 2, "Panel difficulty balance")
            pairs[band][tuple(selected)] += 1
    require(set(exposure.values()) == {3} and exposure.keys() == tasks.keys(), "Unequal task exposure")
    for band, observed in pairs.items():
        expected = set(itertools.combinations(sorted(task_id for task_id in tasks if tasks[task_id]["difficulty"] == band), 2))
        require(set(observed) == expected and set(observed.values()) == {1}, "Within-band pair imbalance")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docs-repo", type=Path, default=Path.home() / "code/github.com/prometheus/docs")
    output = parser.add_mutually_exclusive_group()
    output.add_argument("--tree", choices=REVISIONS, help="print regenerated Markdown, without writing")
    output.add_argument("--inventory", choices=REVISIONS, help="print source, route, rank, and identity audit JSON")
    output.add_argument("--write", action="store_true", help="regenerate only current.md and candidate.md beside this script")
    parser.add_argument("--cross-check-cache", action="store_true", help="also compare the candidate against the optional generated collection")
    args = parser.parse_args()
    collections_by_variant = {variant: collection(args.docs_repo, variant) for variant in REVISIONS}
    if args.cross_check_cache:
        cross_check_cache(args.docs_repo, collections_by_variant["candidate"])
    rendered = {variant: render(docs, variant) for variant, docs in collections_by_variant.items()}
    if args.tree:
        print(rendered[args.tree][0], end="")
        return
    if args.inventory:
        print(json.dumps(rendered[args.inventory][1], ensure_ascii=False, indent=2))
        return
    all_occurrences = set()
    for variant, (markdown, inventory) in rendered.items():
        path = HERE / f"{variant}.md"
        if args.write:
            path.write_text(markdown)
        stored = path.read_text()
        require(stored == markdown, "".join(difflib.unified_diff(stored.splitlines(True), markdown.splitlines(True), fromfile=str(path), tofile="extracted")))
        previous_depth = -1
        for line in markdown.splitlines()[2:]:
            match = LINE.fullmatch(line)
            require(match is not None, f"Invalid Markdown: {line}")
            indent, label, kind, occurrence, content = match.groups()
            depth = len(indent) // 2
            require(len(indent) % 2 == 0 and depth <= previous_depth + 1, "Invalid indentation")
            require((kind == "page") == bool(content), "Invalid target")
            require(occurrence not in all_occurrences, "Duplicate occurrence ID")
            all_occurrences.add(occurrence)
            previous_depth = depth
        pages = sum(node["selectable"] for node in inventory)
        print(f"{variant}: {len(inventory)} nodes, {pages} pages, {len(inventory) - pages} groups; exact source match")
    check_config({variant: value[1] for variant, value in rendered.items()})
    print("Config: 12 reachable tasks; six balanced panels; exposure 3; every within-difficulty pair once")
    if args.cross_check_cache:
        print("Candidate cache: all latest/local routes, labels, ranks, visibility and discovery order match")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(str(error))
