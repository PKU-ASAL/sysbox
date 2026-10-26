#!/usr/bin/env python3
"""Verify a local multi-platform OCI archive's platforms and release labels."""
import argparse, json, tarfile

parser=argparse.ArgumentParser(); parser.add_argument('archive'); parser.add_argument('--version',required=True); parser.add_argument('--revision',required=True); parser.add_argument('--fingerprint',required=True); args=parser.parse_args()
with tarfile.open(args.archive) as tar:
    files={m.name:m for m in tar.getmembers()}
    def load(digest):
        return json.loads(tar.extractfile('blobs/sha256/'+digest.split(':',1)[1]).read())
    outer=json.loads(tar.extractfile('index.json').read())
    if len(outer.get('manifests',[])) != 1 or outer['manifests'][0].get('mediaType') != 'application/vnd.oci.image.index.v1+json':
        raise SystemExit('expected one OCI index descriptor')
    index=load(outer['manifests'][0]['digest'])
    manifests=index.get('manifests',[])
    platforms=sorted((m.get('platform',{}).get('os'),m.get('platform',{}).get('architecture')) for m in manifests)
    if platforms != [('linux','amd64'),('linux','arm64')]: raise SystemExit(f'platform mismatch: {platforms}')
    labels=[]
    for descriptor in manifests:
        manifest=load(descriptor['digest']); config=load(manifest['config']['digest'])
        labels.append(config.get('config',{}).get('Labels',{}))
    expected={'org.opencontainers.image.version':args.version,'org.opencontainers.image.revision':args.revision,'org.opencontainers.image.licenses':'MulanPSL-2.0','io.github.pku-asal.sysbox.release-fingerprint':args.fingerprint}
    if any({key: labels[0].get(key) for key in expected} != expected for _ in [0]): raise SystemExit(f'first label mismatch: {labels[0]}')
    if any({key: label.get(key) for key in expected} != expected for label in labels): raise SystemExit(f'child label mismatch: {labels}')
    print(json.dumps({'platforms':[{'os':os,'architecture':arch} for os,arch in platforms],'labels':expected}))
