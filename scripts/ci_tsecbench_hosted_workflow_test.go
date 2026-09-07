package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTSecBenchHostedWorkflowBuildsAndUploadsTheAMD64Bundle(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "build-tsecbench-hosted.yml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read TSecBench Hosted workflow: %v", err)
	}
	workflow := string(workflowBytes)

	for _, required := range []string{
		"workflow_dispatch:",
		"bundle_version:",
		"runs-on: ubuntu-latest",
		`test "$(uname -m)" = x86_64`,
		"file: docker/tsecbench-hosted/Dockerfile",
		"platforms: linux/amd64",
		"load: true",
		"tags: cyberpenda-tsecbench-hosted:ci",
		"build-args:",
		"RUNTIME_RELEASE_CACHE_BUST=${{ github.run_id }}-${{ github.run_attempt }}-${{ github.sha }}",
		"make smoke-tsecbench-hosted-image",
		"CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c",
		"--network host",
		"CYBERPENDA_REQUIRE_REAL_PI_ACCEPTANCE=1",
		"CYBERPENDA_REAL_PI_ACCEPTANCE_BRIDGE=/usr/local/bin/pentest-provider-bridge",
		"TestHostedAcceptanceConfigurationRunsTheRealPiRuntimeWithTheProjectedSkill",
		`scripts/build-tsecbench-hosted-bundle.sh "${BUNDLE_VERSION}" "${TSECBENCH_HOSTED_IMAGE}" dist/tsecbench-hosted`,
		"path: dist/tsecbench-hosted/cyberpenda-tsecbench-hosted_${{ steps.bundle.outputs.version }}_linux_amd64",
		"if-no-files-found: error",
		"compression-level: 0",
	} {
		assertContains(t, workflow, required)
	}

	for _, forbidden := range []string{
		"docker/setup-qemu-action",
		"secrets.",
		"BENCHMARK_TOKEN",
		"CYBERPENDA_MODEL_API_KEY",
		"validate-tsecbench-local-mode.sh",
		"--privileged",
		"--cap-add",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("TSecBench Hosted workflow must not contain %q", forbidden)
		}
	}

	buildIndex := strings.Index(workflow, "docker/build-push-action@")
	smokeIndex := strings.Index(workflow, "make smoke-tsecbench-hosted-image")
	realPiIndex := strings.Index(workflow, "CYBERPENDA_REQUIRE_REAL_PI_ACCEPTANCE=1")
	bundleIndex := strings.LastIndex(workflow, "scripts/build-tsecbench-hosted-bundle.sh")
	uploadIndex := strings.Index(workflow, "actions/upload-artifact@")
	if !(buildIndex >= 0 && buildIndex < smokeIndex && smokeIndex < realPiIndex && realPiIndex < bundleIndex && bundleIndex < uploadIndex) {
		t.Fatal("TSecBench Hosted workflow steps are not in the safe build, validate, and upload order")
	}
}
