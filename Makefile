.PHONY: fmt test vet check

fmt:
	test -z "$$(gofmt -l .)"

test:
	go test ./...

vet:
	go vet ./...

check: fmt vet test
