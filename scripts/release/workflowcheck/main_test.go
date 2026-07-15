package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCIRejectsReleaseSecret(t *testing.T) {
	raw := []byte("name: CI\non:\n  push:\n    branches: [main]\n  pull_request:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ secrets.RELEASE_TOKEN }}\n")
	require.ErrorContains(t, validateWorkflow("ci", raw), "secret")
}

func TestValidateAcceptanceRequiresManualTriggerAndTrustedRunner(t *testing.T) {
	raw := []byte("name: Acceptance\non:\n  workflow_dispatch:\njobs:\n  acceptance:\n    runs-on: [self-hosted, linux, release]\n    steps:\n      - run: make test-privileged-container\n")
	require.NoError(t, validateWorkflow("acceptance", raw))

	raw = []byte("name: Acceptance\non:\n  push:\njobs:\n  acceptance:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n")
	require.Error(t, validateWorkflow("acceptance", raw))
}

func TestValidateReleaseRequiresTagTriggerAndScopedToken(t *testing.T) {
	raw := []byte("name: Release\non:\n  push:\n    tags: ['v*.*.*']\njobs:\n  verify:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make ci\n  publish:\n    runs-on: [self-hosted, linux, release]\n    steps:\n      - run: scripts/release/build.sh\n      - run: scripts/release/oci.sh build\n      - env:\n          RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n        run: scripts/release/forgejo.sh publish\n")
	require.NoError(t, validateWorkflow("release", raw))

	raw = []byte("name: Release\non:\n  pull_request:\njobs:\n  verify:\n    env:\n      RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}\n    runs-on: ubuntu-latest\n")
	require.Error(t, validateWorkflow("release", raw))
}

func TestValidateWorkflowRejectsMutableActionTags(t *testing.T) {
	raw := []byte("name: CI\non:\n  push:\n    branches: [main]\n  pull_request:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n")
	require.ErrorContains(t, validateWorkflow("ci", raw), "commit SHA")
}
