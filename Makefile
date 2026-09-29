.PHONY: build clean test install-hooks smoke-fast smoke smoke-full

# Build the Go binary.
build:
	cd go && go build -o sonar-migration-tool .

# Run all Go tests.
test:
	cd go && go test ./... -count=1

# Clean build artifacts.
clean:
	rm -f go/sonar-migration-tool

# Install git hooks (pre-commit secret scan via gitleaks).
install-hooks:
	sh scripts/install-git-hooks.sh

# Live smoke suite. Automates docs/REGRESSION-TESTING-PLAN.md.
# TestZZZ is included in every filter so the summary table is always written.
# Tier 0 only: no network, no credentials.
smoke-fast: build
	cd go && go test -tags smoke -count=1 -timeout 10m -v ./smoke/... -run 'TestTier0|TestZZZ'

# Tiers 0, 1 and 3: needs the source SonarQube Server. Makes no SonarQube
# Cloud writes. Tier 3 (gui) needs no credentials at all, so it belongs here
# rather than only in the destructive target.
smoke: build
	cd go && go test -tags smoke -count=1 -timeout 30m -v ./smoke/... -run 'TestTier0|TestTier1|TestTier3|TestZZZ'

# All tiers, including the destructive reset+migrate path. Requires
# SMOKE_ALLOW_DESTRUCTIVE=1 and an allowlisted staging target host.
smoke-full: build
	cd go && go test -tags smoke -count=1 -timeout 90m -v ./smoke/...
