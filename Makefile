all: nb

nb:
	go build -o nb *.go

clean:
	rm -rf nb

.PHONY: all nb initdata clean
