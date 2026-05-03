#!/usr/bin/env bash
# Wire up tracked git hooks for this clone.
# Run once after cloning: ./.githooks/install.sh
set -euo pipefail
git config core.hooksPath .githooks
chmod +x .githooks/pre-commit
echo "git hooks installed (core.hooksPath = .githooks)"
