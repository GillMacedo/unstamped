.PHONY: test lint demo demo-check demo-binary demo-fixed tool

test:
	test -z "$$(gofmt -l plugin tests examples)"
	go vet ./...
	cd examples/stampdemo && go vet ./...
	go test -race -count=1 ./...

lint:
	claude plugin validate ./plugin --strict
	claude plugin validate . --strict

TOOL := bin/unstamped
DEMO_FLAGS := -X example.com/stampdemo/internal/buildinfo.Version=1.4.0 -X example.com/stampdemo/internal/buildinfo.commit=3f2a9c1 -X example.com/stampdemo/internal/buildinfo.BuildDate=2026-11-06 -X example.com/stampdemo/cmd/stampdemo.builtBy=ci

tool:
	@go build -o $(TOOL) ./plugin/scripts

demo: demo-check demo-binary demo-fixed

# The demo flags have three mistakes on purpose, so exit 1 is the expected result.
demo-check: tool
	@$(TOOL) check --dir examples/stampdemo --goos linux --goarch amd64 --ldflags '$(DEMO_FLAGS)' ./cmd/stampdemo || test $$? -eq 1

demo-binary: tool
	@cd examples/stampdemo && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ../../bin/stampdemo -ldflags '$(DEMO_FLAGS)' ./cmd/stampdemo
	@$(TOOL) binary bin/stampdemo || test $$? -eq 1

FIXED_FLAGS := -X example.com/stampdemo/internal/buildinfo.Version=1.4.0 -X example.com/stampdemo/internal/buildinfo.Commit=3f2a9c1 -X main.builtBy=ci

# The same build with the three mistakes fixed. It must stay quiet and exit 0.
demo-fixed: tool
	@$(TOOL) check --dir examples/stampdemo --goos linux --goarch amd64 --ldflags '$(FIXED_FLAGS)' ./cmd/stampdemo
