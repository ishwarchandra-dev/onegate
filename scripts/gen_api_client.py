#!/usr/bin/env python3
"""Generate the typed TypeScript API client from docs/api/openapi.yaml.

Emits (checked in; regenerate with `make api-gen`):
    web/app/lib/api/types.ts   — TS types for every schema + per-op responses
    web/app/lib/api/client.ts  — OneGateClient with one method per operation

The generator covers the JSON-Schema constructs the OneGate spec uses
($ref, object, array, enum, allOf, nullable type arrays,
additionalProperties). Anything else fails loudly so the spec stays
within the supported subset.

Usage:
    python3 scripts/gen_api_client.py           # write files
    python3 scripts/gen_api_client.py --check   # exit 1 when stale (CI)
"""
import sys
from pathlib import Path

import yaml

SPEC = "docs/api/openapi.yaml"
OUT_DIR = Path("web/app/lib/api")

TS_PRIMITIVES = {
    ("string",): "string",
    ("integer",): "number",
    ("number",): "number",
    ("boolean",): "boolean",
}


def ts_name(ref):
    return ref.split("/")[-1]


def null_ok(schema):
    t = schema.get("type")
    if isinstance(t, list) and "null" in t:
        return True
    return False


def base_type(schema):
    t = schema.get("type")
    if isinstance(t, list):
        non_null = [x for x in t if x != "null"]
        return non_null[0] if non_null else "string"
    return t


class Gen:
    def __init__(self, doc):
        self.doc = doc
        self.schemas = doc["components"]["schemas"]
        self.enums = {}      # ts name -> list of literals
        self.interfaces = {} # ts name -> (props, required)
        self.lines_types = []

    # ---------- schema -> TS ----------
    def type_expr(self, schema, indent=0, ref_prefix=""):
        """Return a TS type expression for a schema (inline-safe).

        ref_prefix is prepended to named-schema references ("T." inside
        client.ts, "" inside types.ts).
        """
        if "$ref" in schema:
            return ref_prefix + ts_name(schema["$ref"])
        if "allOf" in schema:
            parts = [self.type_expr(s, indent, ref_prefix) for s in schema["allOf"]]
            return " & ".join(f"({p})" if " & " in p else p for p in parts)
        if "enum" in schema:
            return " | ".join(json_lit(v) for v in schema["enum"])
        kind = base_type(schema)
        if kind == "array":
            return f"Array<{self.type_expr(schema['items'], indent, ref_prefix)}>"
        if kind == "object":
            if schema.get("additionalProperties") and not schema.get("properties"):
                inner = schema["additionalProperties"]
                if inner == True:  # noqa: E712 (YAML bool)
                    return "Record<string, unknown>"
                return f"Record<string, {self.type_expr(inner, indent, ref_prefix)}>"
            # Inline object -> emit into the expression.
            return self.inline_object(schema, indent, ref_prefix)
        return TS_PRIMITIVES.get((kind,), "unknown")

    def inline_object(self, schema, indent, ref_prefix=""):
        props = schema.get("properties", {})
        required = set(schema.get("required", []))
        pad = "  " * (indent + 1)
        close = "  " * indent
        if not props:
            return "Record<string, never>"
        lines = ["{"]
        for name, sub in props.items():
            opt = "" if name in required else "?"
            colon = ":" if name.isidentifier() else ""
            key = name if name.isidentifier() else json.dumps(name)
            te = self.type_expr(sub, indent + 1, ref_prefix)
            if null_ok(sub):
                te = f"({te} | null)"
            lines.append(f"{pad}{key}{opt}{colon} {te}")
        lines.append(close + "}")
        return "\n".join(lines)

    def collect_named(self):
        """Turn every named component schema into an interface or enum."""
        for name, sch in self.schemas.items():
            if "enum" in sch:
                self.enums[name] = [json_lit(v) for v in sch["enum"]]
                continue
            kind = base_type(sch)
            if kind == "object" and sch.get("properties"):
                self.interfaces[name] = (sch, set(sch.get("required", [])))
            # primitives like UnixMS/MicroUSD get type aliases inline below.

    # ---------- per-operation types ----------
    def op_response(self, op):
        """Return the TS expression for the 200/201 response."""
        responses = op.get("responses", {})
        for code in ("200", "201"):
            if code not in responses:
                continue
            content = responses[code].get("content", {})
            json_content = content.get("application/json")
            if not json_content:
                continue
            return self.type_expr(json_content["schema"], 0, "")
        return "void"

    def path_params(self, path):
        import re
        return re.findall(r"\{(\w+)\}", path)

    def op_query_params(self, path_item, method):
        params = []
        for p in path_item.get("parameters", []):
            params.append(self.resolve_param(p))
        for p in (path_item.get(method, {}) or {}).get("parameters", []):
            params.append(self.resolve_param(p))
        # dedupe by name, keep only query params
        seen, out = set(), []
        for p in params:
            if p and p.get("in") == "query" and p["name"] not in seen:
                seen.add(p["name"])
                out.append(p)
        return out

    def resolve_param(self, p):
        if "$ref" in p:
            return self.doc["components"]["parameters"][ts_name(p["$ref"])]
        return p

    def body_type(self, op):
        rb = op.get("requestBody")
        if not rb:
            return None
        content = rb.get("content", {}).get("application/json")
        if not content:
            return None
        return self.type_expr(content["schema"], 0, "T.")


def json_lit(v):
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, (int, float)):
        return str(v)
    return f'"{v}"'


def camel(name):
    parts = name.split("_")
    return parts[0] + "".join(p.capitalize() for p in parts[1:])


def main():
    check = "--check" in sys.argv
    doc = yaml.safe_load(open(SPEC))
    g = Gen(doc)
    g.collect_named()

    # ---------------- types.ts ----------------
    t = []
    t.append("// Code generated by scripts/gen_api_client.py from docs/api/openapi.yaml.")
    t.append("// DO NOT EDIT BY HAND — regenerate with `make api-gen`.")
    t.append("")
    t.append("// ---------------------------------------------------------------------")
    t.append("// Component schemas")
    t.append("// ---------------------------------------------------------------------")
    for name in g.schemas:
        sch = g.schemas[name]
        if name in g.enums:
            t.append(f"export type {name} = {' | '.join(g.enums[name])}")
            continue
        kind = base_type(sch)
        if kind == "object" and sch.get("properties"):
            t.append("export interface " + name + " {")
            for pname, sub in sch["properties"].items():
                opt = "" if pname in set(sch.get("required", [])) else "?"
                te = g.type_expr(sub, 1)
                if null_ok(sub):
                    te = f"({te} | null)"
                t.append(f"  {pname}{opt}: {te}")
            t.append("}")
        elif kind == "array":
            t.append(f"export type {name} = {g.type_expr(sch)}")
        else:
            t.append(f"export type {name} = {g.type_expr(sch)}")
        t.append("")

    # Per-operation request/response types.
    t.append("// ---------------------------------------------------------------------")
    t.append("// Operation types (responses, query params)")
    t.append("// ---------------------------------------------------------------------")
    ops = []
    for path, item in doc["paths"].items():
        for method, op in item.items():
            if method in ("parameters",) or method.startswith("x-"):
                continue
            ops.append((path, method, op))

    for path, method, op in sorted(ops, key=lambda x: x[2]["operationId"]):
        oid = op["operationId"]
        resp = g.op_response(op)
        t.append(f"export type {pascal(oid)}Response = {resp}")
        qp = g.op_query_params(doc["paths"][path], method)
        if qp:
            t.append(f"export interface {pascal(oid)}Params {{")
            for p in qp:
                opt = "" if p.get("required") else "?"
                te = g.type_expr(p.get("schema", {"type": "string"}), 1)
                t.append(f"  {p['name']}{opt}: {te}")
            t.append("}")
        t.append("")

    types_ts = "\n".join(t) + "\n"

    # ---------------- client.ts ----------------
    c = []
    c.append("// Code generated by scripts/gen_api_client.py from docs/api/openapi.yaml.")
    c.append("// DO NOT EDIT BY HAND — regenerate with `make api-gen`.")
    c.append("")
    c.append('import type * as T from "./types"')
    c.append("")
    c.append("/**")
    c.append(" * Error shape from the management API: the wire envelope's")
    c.append(" * `error` object, raised as an exception.")
    c.append(" */")
    c.append("export class ApiError extends Error {")
    c.append("  status: number")
    c.append("  type: string")
    c.append("  code?: string")
    c.append("  param?: string")
    c.append("  retryable: boolean")
    c.append("")
    c.append("  constructor(fields: {")
    c.append("    status: number")
    c.append("    type: string")
    c.append("    code?: string")
    c.append("    param?: string")
    c.append("    retryable: boolean")
    c.append("    message: string")
    c.append("  }) {")
    c.append("    super(fields.message)")
    c.append("    this.name = \"ApiError\"")
    c.append("    this.status = fields.status")
    c.append("    this.type = fields.type")
    c.append("    this.code = fields.code")
    c.append("    this.param = fields.param")
    c.append("    this.retryable = fields.retryable")
    c.append("  }")
    c.append("}")
    c.append("")
    c.append("export interface ClientOptions {")
    c.append("  /** Base URL; empty means same-origin. */")
    c.append("  baseUrl?: string")
    c.append("  /** CSRF token for mutating calls (from GET /api/auth/session). */")
    c.append("  csrfToken?: string")
    c.append("  /** Custom fetch (tests). */")
    c.append("  fetchFn?: typeof fetch")
    c.append("}")
    c.append("")
    c.append("const MUTATING = new Set([\"POST\", \"PUT\", \"PATCH\", \"DELETE\"])")
    c.append("")
    c.append("export class OneGateClient {")
    c.append("  private baseUrl: string")
    c.append("  private csrfToken: string")
    c.append("  private fetchFn: typeof fetch")
    c.append("")
    c.append("  constructor(opts: ClientOptions = {}) {")
    c.append("    this.baseUrl = opts.baseUrl ?? \"\"")
    c.append("    this.csrfToken = opts.csrfToken ?? \"\"")
    c.append("    this.fetchFn = opts.fetchFn ?? fetch.bind(globalThis)")
    c.append("  }")
    c.append("")
    c.append("  setCsrfToken(token: string) {")
    c.append("    this.csrfToken = token")
    c.append("  }")
    c.append("")
    c.append("  private async request<R>(")
    c.append("    method: string,")
    c.append("    path: string,")
    c.append("    init: { query?: Record<string, string | number | undefined>; body?: unknown } = {}")
    c.append("  ): Promise<R> {")
    c.append("    const url = new URL(this.baseUrl + path, globalThis.location?.origin ?? \"http://localhost\")")
    c.append("    for (const [k, v] of Object.entries(init.query ?? {})) {")
    c.append("      if (v !== undefined) url.searchParams.set(k, String(v))")
    c.append("    }")
    c.append("    const headers: Record<string, string> = {}")
    c.append("    if (init.body !== undefined) headers[\"Content-Type\"] = \"application/json\"")
    c.append("    if (MUTATING.has(method) && this.csrfToken) headers[\"X-CSRF-Token\"] = this.csrfToken")
    c.append("    const res = await this.fetchFn(url.toString(), {")
    c.append("      method,")
    c.append("      headers,")
    c.append("      credentials: \"same-origin\",")
    c.append("      body: init.body === undefined ? undefined : JSON.stringify(init.body),")
    c.append("    })")
    c.append("    if (res.status === 204) return undefined as R")
    c.append("    const payload = await res.json().catch(() => null)")
    c.append("    if (!res.ok) {")
    c.append("      const e = (payload as { error?: Record<string, unknown> } | null)?.error")
    c.append("      throw new ApiError({")
    c.append("        status: res.status,")
    c.append("        type: (e?.type as string) ?? \"api_error\",")
    c.append("        code: e?.code as string | undefined,")
    c.append("        param: e?.param as string | undefined,")
    c.append("        retryable: (e?.retryable as boolean | undefined) ?? false,")
    c.append("        message: (e?.message as string | undefined) ?? res.statusText,")
    c.append("      })")
    c.append("    }")
    c.append("    return payload as R")
    c.append("  }")

    for path, method, op in sorted(ops, key=lambda x: x[2]["operationId"]):
        oid = op["operationId"]
        pp = g.path_params(path)
        qp = [p["name"] for p in g.op_query_params(doc["paths"][path], method)]
        bt = g.body_type(op)
        rt = f"T.{pascal(oid)}Response"

        args = []
        for p in pp:
            args.append(f"{p}: string")
        params_arg = ""
        if qp:
            params_arg = f"params?: T.{pascal(oid)}Params"
        body_arg = ""
        if bt and bt != "null":
            body_arg = f"body: {bt}"
        arg_list = [a for a in (params_arg, body_arg) if a]
        sig = f"  {camel(oid)}({', '.join(args + arg_list)}): Promise<{rt}> {{"
        c.append("")
        c.append(f"  /** {op.get('summary', oid)} ({method.upper()} {path}) */")
        c.append(sig)
        # Build the path with substitutions.
        rp = path
        for p in pp:
            rp = rp.replace("{" + p + "}", f"${{{p}}}")
        rp = f"`{rp}`"
        q = ""
        if qp:
            entries = ", ".join(f'"{n}": params?.{n}' for n in qp)
            q = f", {{ query: {{ {entries} }} }}"
        b = ""
        if bt and bt != "null":
            b = ", { body }"
        c.append(f"    return this.request<{rt}>(\"{method.upper()}\", {rp}{q}{b})")
        c.append("  }")

    c.append("}")
    client_ts = "\n".join(c) + "\n"

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    outputs = {
        OUT_DIR / "types.ts": types_ts,
        OUT_DIR / "client.ts": client_ts,
    }
    if check:
        stale = [str(p) for p, content in outputs.items()
                 if not p.exists() or p.read_text() != content]
        if stale:
            print(f"STALE: {', '.join(stale)} — run `make api-gen`")
            return 1
        print(f"OK: web client up to date with {SPEC} ({len(ops)} operations)")
        return 0
    for p, content in outputs.items():
        p.write_text(content)
    print(f"wrote {OUT_DIR}/types.ts and client.ts ({len(ops)} operations)")
    return 0


def pascal(oid):
    # operationIds are camelCase; export names capitalize the first letter.
    return oid[0].upper() + oid[1:]


if __name__ == "__main__":
    sys.exit(main())
