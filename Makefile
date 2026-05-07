.PHONY: docs-manifest

# Generate the CLI documentation manifest consumed by justtunnel-landing.
# See README "Generating the docs manifest" for the full workflow.
docs-manifest:
	@mkdir -p dist
	@go run ./cmd/docsgen > dist/cli-manifest.json
	@echo "wrote dist/cli-manifest.json"
	@jq '.commands | length' dist/cli-manifest.json
