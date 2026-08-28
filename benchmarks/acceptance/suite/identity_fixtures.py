from __future__ import annotations

from pathlib import Path

import evidence_identity


def current_binding(repository: Path, candidate: str, scopes: dict[str, dict[str, str]]) -> dict:
    """Build a minimal valid v6 binding for Suite unit tests."""
    binary_identities = {
        value["artifact_sha256"] for name, value in scopes.items() if name != "frontier"
    }
    binary_identity = binary_identities.pop() if binary_identities else "f" * 64
    if binary_identities:
        raise ValueError("fixture binary scopes must share one artifact")
    components = {
        role: {
            "identity": binary_identity if role == "binary" else evidence_identity.canonical_sha256({"role": role}),
            "direct_dependencies": {},
        }
        for role in evidence_identity.COMPONENT_ROLES
    }
    manifests = {
        f"{scope}-tools.json": {
            "schema": "fixture-tool", "scope": scope,
            "files": [{
                "path": f"fixture/{scope}.py",
                "sha256": evidence_identity.canonical_sha256({"scope": scope}),
            }],
        }
        for scope in scopes
    }
    return evidence_identity.build_current_binding(
        candidate=candidate,
        suite_version="1.0.0",
        scopes=scopes,
        components=components,
        manifests=manifests,
        lifecycle=evidence_identity.lifecycle_identities(repository),
        reporting=evidence_identity.reporting_identities(repository),
        audit={"source_git": candidate},
    )
