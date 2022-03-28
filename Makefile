all: nb initdata

nb:
	go build -o nb *.go

initdata:
	go build -o initdata tools/initdata.go

clean:
	rm -rf nb initdata

.PHONY: all nb initdata clean
