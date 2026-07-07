package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppDockerfileBuildsEmbeddedUIDaemonImage(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerfilePath := filepath.Join(repoRoot, "docker", "pentestd", "Dockerfile")
	dockerfileBytes, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read app Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	assertContains(t, dockerfile, "FROM --platform=$BUILDPLATFORM node:20")
	assertContains(t, dockerfile, "npm ci")
	assertContains(t, dockerfile, "npm run build")
	assertContains(t, dockerfile, "FROM --platform=$BUILDPLATFORM golang:1.25")
	assertContains(t, dockerfile, "COPY --from=web-build /src/web/dist internal/daemon/webfs/dist")
	assertContains(t, dockerfile, "CGO_ENABLED=0")
	assertContains(t, dockerfile, "GOOS=${TARGETOS}")
	assertContains(t, dockerfile, "GOARCH=${TARGETARCH}")
	assertContains(t, dockerfile, "-X main.version=${VERSION}")
	assertContains(t, dockerfile, "./cmd/pentestd")
	assertContains(t, dockerfile, "PENTEST_LISTEN_ADDR=0.0.0.0:8787")
	assertContains(t, dockerfile, "PENTEST_DB=/data/pentest.db")
	assertContains(t, dockerfile, "PENTEST_RUNTIME_ROOT=/data/runs")
	assertContains(t, dockerfile, "EXPOSE 8787")
	assertContains(t, dockerfile, `ENTRYPOINT ["/usr/local/bin/pentestd"]`)
}

func TestDockerignoreKeepsAppImageContextSmall(t *testing.T) {
	repoRoot := repoRoot(t)
	dockerignorePath := filepath.Join(repoRoot, ".dockerignore")
	dockerignoreBytes, err := os.ReadFile(dockerignorePath)
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	dockerignore := string(dockerignoreBytes)

	for _, ignored := range []string{".git", ".cache", "web/node_modules", "web/dist", "internal/daemon/webfs/dist", "pentest.db", "runs"} {
		assertContains(t, dockerignore, ignored)
	}
}

func TestDockerComposeRunsPublishedAppImage(t *testing.T) {
	repoRoot := repoRoot(t)
	composePath := filepath.Join(repoRoot, "docker-compose.yaml")
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read docker-compose.yaml: %v", err)
	}
	compose := string(composeBytes)

	assertContains(t, compose, "name: cyberpenda")
	assertContains(t, compose, "cyberpenda:")
	assertContains(t, compose, "image: ghcr.io/n1majne3/cyberpenda:${CYBERPENDA_IMAGE_TAG:-latest}")
	assertContains(t, compose, "restart: unless-stopped")
	assertContains(t, compose, "init: true")
	assertContains(t, compose, `"${CYBERPENDA_BIND:-127.0.0.1}:${CYBERPENDA_PORT:-8787}:8787"`)
	assertContains(t, compose, "PENTEST_AUTH_TOKEN: ${PENTEST_AUTH_TOKEN:?Set PENTEST_AUTH_TOKEN before starting CyberPenda}")
	assertContains(t, compose, "PENTEST_LISTEN_ADDR: 0.0.0.0:8787")
	assertContains(t, compose, "PENTEST_DB: /data/pentest.db")
	assertContains(t, compose, "PENTEST_RUNTIME_ROOT: /data/runs")
	assertContains(t, compose, "PENTEST_SANDBOX_IMAGE: ${PENTEST_SANDBOX_IMAGE:-ghcr.io/n1majne3/cyberpenda-sandbox:latest}")
	assertContains(t, compose, "PENTEST_CONTAINER_CLI: docker")
	assertContains(t, compose, "cyberpenda-data:/data")
	assertContains(t, compose, "/var/run/docker.sock:/var/run/docker.sock")
	assertContains(t, compose, "no-new-privileges:true")
	assertContains(t, compose, "healthcheck:")
	assertContains(t, compose, "volumes:")
	assertContains(t, compose, "cyberpenda-data:")
}

func TestCIWorkflowBuildsAppImage(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "ci.yml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	workflow := string(workflowBytes)

	assertContains(t, workflow, "app-image:")
	assertContains(t, workflow, "docker/setup-buildx-action@v4")
	assertContains(t, workflow, "docker/build-push-action@v7")
	assertContains(t, workflow, "file: docker/pentestd/Dockerfile")
	assertContains(t, workflow, "platforms: linux/amd64")
	assertContains(t, workflow, "push: false")
	assertContains(t, workflow, "VERSION=ci")
}

func TestReleaseWorkflowPublishesAppImage(t *testing.T) {
	repoRoot := repoRoot(t)
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	workflowBytes, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(workflowBytes)

	assertContains(t, workflow, "publish-app-image:")
	assertContains(t, workflow, `image_name="$(echo "${GITHUB_REPOSITORY}" | tr '[:upper:]' '[:lower:]')"`)
	assertContains(t, workflow, "ghcr.io/${image_name}")
	assertContains(t, workflow, "file: docker/pentestd/Dockerfile")
	assertContains(t, workflow, "platforms: linux/amd64,linux/arm64")
	assertContains(t, workflow, "push: true")
	assertContains(t, workflow, "VERSION=${{ github.ref_name }}")
	assertContains(t, workflow, "org.opencontainers.image.title=CyberPenda")

	if strings.Contains(workflow, "publish-app-image:\n    needs: publish-sandbox-image") {
		t.Fatal("app image release should not wait for the large sandbox image build")
	}
}
