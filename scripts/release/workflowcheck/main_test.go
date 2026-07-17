package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const validRelease = "name: Release\npermissions:\n  contents: read\non:\n  push:\n    tags: ['v*.*.*']\njobs:\n  verify:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make ci\n  publish:\n    needs: verify\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      packages: write\n    steps:\n      - uses: docker/setup-qemu-action@29109295f81e9208d7d86ff1c6c12d2833863392\n      - uses: docker/setup-buildx-action@e468171a9de216ec08956ac3ada2f0791b6bd435\n      - run: scripts/release/build.sh\n      - env:\n          GHCR_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n        run: docker login ghcr.io\n      - run: scripts/release/oci.sh build --image ghcr.io/pku-asal/sysbox --metadata-field oci_digest\n      - run: scripts/release/oci.sh build --image ghcr.io/pku-asal/sysbox-cli --dockerfile Dockerfile.cli --metadata-field cli_oci_digest\n      - run: scripts/release/oci.sh build --image ghcr.io/pku-asal/sysbox-metadata --dockerfile Dockerfile.metadata --metadata-field metadata_oci_digest\n"

func TestValidateCIRequiresHostedRunnerAndNoSecrets(t *testing.T) {
	raw := []byte("name: CI\non:\n  push:\n    branches: [main]\n  pull_request:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n")
	require.NoError(t, validateWorkflow("ci", raw))

	raw = []byte("name: CI\non:\n  push:\n  pull_request:\njobs:\n  test:\n    runs-on: self-hosted\n    steps:\n      - run: echo ${{ secrets.RELEASE_TOKEN }}\n")
	require.Error(t, validateWorkflow("ci", raw))
}

func TestValidateReleaseRequiresHostedGHCRPublication(t *testing.T) {
	require.NoError(t, validateWorkflow("release", []byte(validRelease)))
}

func TestValidateReleaseRequiresPackageWritePermission(t *testing.T) {
	raw := []byte(strings.Replace(validRelease, "packages: write", "packages: read", 1))
	require.ErrorContains(t, validateWorkflow("release", raw), "packages: write")
}

func TestValidateReleaseRejectsSelfHostedRunner(t *testing.T) {
	raw := []byte(strings.ReplaceAll(validRelease, "ubuntu-latest", "self-hosted"))
	require.ErrorContains(t, validateWorkflow("release", raw), "ubuntu-latest")
}

func TestValidateReleaseRequiresQEMUAndBuildx(t *testing.T) {
	raw := []byte(strings.Replace(validRelease, "      - uses: docker/setup-qemu-action@29109295f81e9208d7d86ff1c6c12d2833863392\n", "", 1))
	require.ErrorContains(t, validateWorkflow("release", raw), "QEMU")
}

func TestValidateReleaseRequiresCLIAndMetadataDockerfiles(t *testing.T) {
	withoutCLI := []byte(strings.Replace(validRelease, "--dockerfile Dockerfile.cli ", "", 1))
	require.ErrorContains(t, validateWorkflow("release", withoutCLI), "Dockerfile.cli")
}

func TestValidateWorkflowRejectsMutableActionTags(t *testing.T) {
	raw := []byte("name: CI\non:\n  push:\n    branches: [main]\n  pull_request:\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n")
	require.ErrorContains(t, validateWorkflow("ci", raw), "commit SHA")
}
