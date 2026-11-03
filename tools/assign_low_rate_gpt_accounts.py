#!/usr/bin/env python3
"""Add low-rate OpenAI API-key accounts to an existing group.

Dry-run is the default. Set SUB2API_BASE_URL and SUB2API_ADMIN_TOKEN, then pass
--apply to write changes through the admin API.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from decimal import Decimal, InvalidOperation
from typing import Any


def request_json(base_url: str, token: str, path: str, query: dict[str, Any] | None = None,
                 method: str = "GET", payload: dict[str, Any] | None = None) -> Any:
    url = base_url.rstrip("/") + "/api/v1" + path
    if query:
        url += "?" + urllib.parse.urlencode(query)
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(url, data=data, method=method)
    request.add_header("Authorization", "Bearer " + token)
    request.add_header("Accept", "application/json")
    if data is not None:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            decoded = json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"{method} {path} returned HTTP {exc.code}: {detail}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"{method} {path} failed: {exc.reason}") from exc

    if not isinstance(decoded, dict) or decoded.get("code") not in (0, None):
        raise RuntimeError(f"{method} {path} failed: {decoded}")
    return decoded.get("data", decoded)


def list_accounts(base_url: str, token: str, platform: str, page_size: int) -> list[dict[str, Any]]:
    accounts: list[dict[str, Any]] = []
    page = 1
    while True:
        result = request_json(
            base_url,
            token,
            "/admin/accounts",
            {"page": page, "page_size": page_size, "platform": platform, "type": "apikey", "status": "active"},
        )
        if not isinstance(result, dict) or not isinstance(result.get("items"), list):
            raise RuntimeError("Unexpected account list response; expected paginated data.items")
        accounts.extend(item for item in result["items"] if isinstance(item, dict))
        if len(accounts) >= int(result.get("total", len(accounts))) or not result["items"]:
            return accounts
        page += 1


def list_groups(base_url: str, token: str, platform: str) -> list[dict[str, Any]]:
    result = request_json(base_url, token, "/admin/groups/all", {"platform": platform})
    if not isinstance(result, list):
        raise RuntimeError("Unexpected group list response; expected an array")
    return [item for item in result if isinstance(item, dict)]


def numeric_rate(account: dict[str, Any]) -> Decimal | None:
    value = account.get("rate_multiplier")
    if value is None:
        return None
    try:
        rate = Decimal(str(value))
    except (InvalidOperation, ValueError):
        return None
    return rate if rate.is_finite() and rate >= 0 else None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", default="openai", help="Account/group platform (default: openai)")
    parser.add_argument("--group-name", default="gpt0.08", help="Existing destination group name")
    parser.add_argument("--threshold", default="0.08", help="Strict upper rate bound (default: 0.08)")
    parser.add_argument("--priority-start", type=int, default=10,
                        help="Priority for the lowest distinct rate; lower values schedule first (default: 10)")
    parser.add_argument("--page-size", type=int, default=100)
    parser.add_argument("--apply", action="store_true", help="Apply changes; otherwise only preview")
    args = parser.parse_args()

    base_url = os.environ.get("SUB2API_BASE_URL", "").strip()
    token = os.environ.get("SUB2API_ADMIN_TOKEN", "").strip()
    if not base_url or not token:
        parser.error("set SUB2API_BASE_URL and SUB2API_ADMIN_TOKEN")
    try:
        threshold = Decimal(args.threshold)
    except InvalidOperation:
        parser.error("--threshold must be a number")
    if not threshold.is_finite() or threshold <= 0:
        parser.error("--threshold must be greater than zero")
    if args.priority_start < 0 or args.page_size < 1:
        parser.error("--priority-start must be >= 0 and --page-size must be >= 1")

    try:
        groups = list_groups(base_url, token, args.platform)
        matches = [group for group in groups if group.get("name", "").casefold() == args.group_name.casefold()]
        if len(matches) != 1:
            raise RuntimeError(
                f"expected exactly one active {args.platform} group named {args.group_name!r}; found {len(matches)}"
            )
        target_group = matches[0]
        group_id = int(target_group["id"])

        accounts = list_accounts(base_url, token, args.platform, args.page_size)
        candidates: list[tuple[dict[str, Any], Decimal]] = []
        for account in accounts:
            rate = numeric_rate(account)
            if rate is not None and rate < threshold:
                candidates.append((account, rate))
        candidates.sort(key=lambda item: (item[1], int(item[0].get("id", 0))))

        priorities_by_rate: dict[Decimal, int] = {}
        for _, rate in candidates:
            if rate not in priorities_by_rate:
                priorities_by_rate[rate] = args.priority_start + len(priorities_by_rate)

        changes: list[tuple[dict[str, Any], list[int], int, Decimal]] = []
        for account, rate in candidates:
            group_ids = [int(value) for value in (account.get("group_ids") or [])]
            next_group_ids = group_ids if group_id in group_ids else [*group_ids, group_id]
            priority = priorities_by_rate[rate]
            if next_group_ids != group_ids or account.get("priority") != priority:
                changes.append((account, next_group_ids, priority, rate))

        action = "APPLY" if args.apply else "DRY RUN"
        print(f"{action}: platform={args.platform}, destination={args.group_name} (id={group_id}), rate<{threshold}")
        print(f"Matched {len(candidates)} active API-key account(s); {len(changes)} update(s) needed.")
        for account, next_group_ids, priority, rate in changes:
            name = str(account.get("name") or f"account-{account.get('id')}")
            already_bound = group_id in [int(value) for value in (account.get("group_ids") or [])]
            operation = "priority only" if already_bound else "add group, set priority"
            print(f"  id={account.get('id')} name={name!r} rate={rate} priority={priority} action={operation}")

        if not args.apply:
            print("No changes made. Re-run with --apply to apply this preview.")
            return 0

        for account, next_group_ids, priority, _ in changes:
            request_json(
                base_url,
                token,
                f"/admin/accounts/{int(account['id'])}",
                method="PUT",
                payload={"group_ids": next_group_ids, "priority": priority},
            )
            print(f"  updated account id={account['id']}")
            time.sleep(0.05)
        print(f"Applied {len(changes)} update(s).")
        return 0
    except (RuntimeError, KeyError, TypeError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
