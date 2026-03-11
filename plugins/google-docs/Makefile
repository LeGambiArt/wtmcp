HANDLER    := handler
REPO_ROOT  := $(shell git rev-parse --show-toplevel)
PLUGIN     := $(notdir $(CURDIR))

VERSION    ?= $(shell cat $(REPO_ROOT)/VERSION 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS    := -s -w -X main.Version=$(VERSION) -X main.BuildDate=$(BUILD_DATE)

.PHONY: build clean

build:
	go build -C $(REPO_ROOT) -ldflags "$(LDFLAGS)" -o plugins/$(PLUGIN)/$(HANDLER) ./plugins/$(PLUGIN)

clean:
	rm -f $(HANDLER)
