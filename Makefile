COVERPROFILE ?= cover.out
COVERPROFILE_LIB ?= cover.lib.out
COVER_THRESHOLD ?= 90

.PHONY: all build test lint fmt vet coverprofile cover cover-html cover-check clean

all: build test lint

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run

fmt:
	go fmt ./...

vet:
	go vet ./...

# Run the full test suite with coverage. -coverpkg=./... is required so that the
# integrationtests package is credited with the library code it exercises. The
# examples are main packages with no tests so they are stripped from the profile
# the reports are built from.
coverprofile:
	go test -covermode=atomic -coverpkg=./... -coverprofile=$(COVERPROFILE) ./...
	grep -v '/examples/' $(COVERPROFILE) > $(COVERPROFILE_LIB)

cover: coverprofile
	@go tool cover -func=$(COVERPROFILE_LIB)

cover-html: coverprofile
	go tool cover -html=$(COVERPROFILE_LIB)

cover-check: coverprofile
	@total=$$(go tool cover -func=$(COVERPROFILE_LIB) | awk '/^total:/ {print $$NF}' | tr -d '%'); \
	echo "total coverage: $$total% (threshold: $(COVER_THRESHOLD)%)"; \
	awk -v t="$$total" -v min=$(COVER_THRESHOLD) 'BEGIN { if (t+0 < min+0) { printf "coverage %s%% is below threshold %s%%\n", t, min; exit 1 } }'

clean:
	rm -f $(COVERPROFILE) $(COVERPROFILE_LIB)
