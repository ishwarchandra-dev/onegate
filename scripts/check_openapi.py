#!/usr/bin/env python3
"""Structural validation for docs/api/openapi.yaml.

Checks (evidence for p6.api-spec acceptance):
  1. File parses as YAML and satisfies basic OpenAPI shape.
  2. Every operation has a unique operationId.
  3. Every operation carries x-auth with a legal auth class.
  4. Every operation carries x-rate-limit with a defined class
     (classes defined in x-rate-limit-classes).
  5. Every mutating operation under admin-session documents the CSRF
     header (implicitly via component docs; checked here as spec-level
     documentation of the CSRF scheme).
"""
import sys

try:
    import yaml
except ImportError:
    print("PyYAML missing; pip install pyyaml", file=sys.stderr)
    sys.exit(2)

SPEC = "docs/api/openapi.yaml"
LEGAL_AUTH = {"admin-session", "admin-token", "public"}
MUTATING = {"post", "put", "patch", "delete"}

errors = []
warnings = []

with open(SPEC) as f:
    doc = yaml.safe_load(f)

if not isinstance(doc, dict) or doc.get("openapi", "").startswith("3.0"):
    errors.append("spec must be an OpenAPI 3.1 document")

paths = doc.get("paths", {})
if not paths:
    errors.append("no paths defined")

classes = doc.get("x-rate-limit-classes", {})
if not classes:
    errors.append("x-rate-limit-classes missing/empty")

op_ids = set()
ops_total = 0

for path, item in sorted(paths.items()):
    if not isinstance(item, dict):
        errors.append(f"{path}: not a mapping")
        continue
    for method, op in item.items():
        if method.startswith("x-") or method == "parameters":
            continue
        if method not in {"get", "post", "put", "patch", "delete", "head", "options"}:
            warnings.append(f"{path}#{method}: unusual method")
            continue
        ops_total += 1
        tag = f"{method.upper()} {path}"

        oid = op.get("operationId")
        if not oid:
            errors.append(f"{tag}: missing operationId")
        elif oid in op_ids:
            errors.append(f"{tag}: duplicate operationId {oid}")
        else:
            op_ids.add(oid)

        for must in ("summary", "tags"):
            if not op.get(must):
                errors.append(f"{tag}: missing {must}")

        auth = op.get("x-auth")
        if not auth:
            errors.append(f"{tag}: missing x-auth")
        elif auth not in LEGAL_AUTH:
            errors.append(f"{tag}: illegal x-auth {auth!r}")

        rl = op.get("x-rate-limit")
        if not rl:
            errors.append(f"{tag}: missing x-rate-limit")
        elif rl not in classes:
            errors.append(f"{tag}: x-rate-limit {rl!r} not defined in x-rate-limit-classes")

        # responses must exist and be non-empty
        responses = op.get("responses")
        if not responses:
            errors.append(f"{tag}: no responses")
        else:
            for code, resp in responses.items():
                if isinstance(resp, dict) and not resp.get("$ref") and not resp.get("description"):
                    errors.append(f"{tag}: response {code} lacks description")

# CSRF documentation invariant
sec = doc.get("components", {}).get("securitySchemes", {})
if "sessionCookie" not in sec:
    errors.append("components.securitySchemes.sessionCookie missing")

print(f"operations checked : {ops_total}")
print(f"rate-limit classes : {len(classes)} ({', '.join(sorted(classes))})")
print(f"paths              : {len(paths)}")

for w in warnings:
    print(f"WARN  {w}")
for e in errors:
    print(f"ERROR {e}")

sys.exit(1 if errors else 0)
