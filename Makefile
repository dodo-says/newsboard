all: nb initdata

nb: nb.go
	go build -o nb nb.go

initdata: tools/initdata.go
	go build -o initdata tools/initdata.go

clean:
	rm -rf nb initdata

