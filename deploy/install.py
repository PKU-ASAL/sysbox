#!/usr/bin/env python3
"""Install one verified Sysbox bundle without replacing live identities."""
from __future__ import annotations
import argparse, hashlib, json, os, platform, shutil, stat
from pathlib import Path

REQUIRED = ("sysbox", "sysbox-init", "sysbox-netns", "deploy/systemd/sysbox-host.service", "deploy/systemd/sysbox-agent.service", "deploy/docker/agent.yaml", "build-metadata.json")

def machine_arch():
    value = platform.machine().lower()
    return {"x86_64":"amd64", "aarch64":"arm64"}.get(value, value)

def _checksums(bundle):
    sums = bundle / "INSTALL-SHA256SUMS"
    if not sums.is_file(): raise ValueError("checksum manifest missing")
    got = {}
    for line in sums.read_text().splitlines():
        line=line.strip()
        if not line: continue
        parts=line.split(None,1)
        if len(parts)!=2: raise ValueError("invalid checksum manifest")
        digest,name=parts
        if len(digest)!=64: raise ValueError("invalid checksum entry")
        got[name.lstrip("* ")] = digest
    for name in REQUIRED:
        path=bundle/name
        if not path.is_file() or got.get(name) != hashlib.sha256(path.read_bytes()).hexdigest():
            raise ValueError(f"checksum mismatch or missing: {name}")
    return got

def _validate(bundle, inputs):
    _checksums(bundle)
    meta=json.loads((bundle/"build-metadata.json").read_text())
    if meta.get("os") != "linux" or meta.get("architecture") != machine_arch(): raise ValueError("bundle architecture mismatch")
    identity=json.loads((inputs/"identity.json").read_text())
    if identity.get("token") or not all(identity.get(k) for k in ("id","api_url","secret")): raise ValueError("identity must be a worker identity without administrator token")
    for name in ("host.key","host.pub","issuer.pub","sysbox.yaml"):
        if not (inputs/name).is_file(): raise ValueError(f"missing input: {name}")

def install(bundle, inputs, root, check=False):
    bundle, inputs, root = map(Path, (bundle, inputs, root))
    _validate(bundle, inputs)
    if check: return
    if root.exists(): raise ValueError(f"destination has existing files: {root}")
    stage=root.with_name(root.name+".new")
    if stage.exists(): raise ValueError(f"destination staging path already exists: {stage}")
    try:
        for rel in REQUIRED:
            dst=stage/"usr/local/bin"/Path(rel).name if rel in ("sysbox","sysbox-init","sysbox-netns") else stage/"etc/sysbox"/Path(rel).name
            dst.parent.mkdir(parents=True, exist_ok=True); shutil.copy2(bundle/rel,dst)
            if rel in ("sysbox","sysbox-init","sysbox-netns"): dst.chmod(0o755)
        for name in ("host.key","host.pub","issuer.pub","sysbox.yaml"):
            dst=stage/("etc/sysbox-host" if name in ("host.key","sysbox.yaml") else "etc/sysbox-worker")/name; dst.parent.mkdir(parents=True,exist_ok=True); shutil.copy2(inputs/name,dst); dst.chmod(0o600 if name in ("host.key","sysbox.yaml") else 0o444)
        (stage/"var/lib/sysbox-agent").mkdir(parents=True)
        shutil.copy2(inputs/"identity.json",stage/"var/lib/sysbox-agent/identity.json"); (stage/"var/lib/sysbox-agent/identity.json").chmod(0o600)
        stage.rename(root)
    except Exception:
        shutil.rmtree(stage, ignore_errors=True); raise

def main():
    ap=argparse.ArgumentParser(); ap.add_argument("bundle"); ap.add_argument("--inputs",required=True); ap.add_argument("--root",default="/"); ap.add_argument("--check",action="store_true"); args=ap.parse_args(); install(args.bundle,args.inputs,args.root,args.check)
if __name__ == "__main__": main()
