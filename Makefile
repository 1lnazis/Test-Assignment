.PHONY: fmt vet test

fmt:
	gofmt -s -w .

vet:
	go vet ./...

test:
	go test -race -count=1 ./...
