package thunderpb

//go:generate sh -c "docker run -v `pwd`:/defs namely/protoc-all:1.11 -d . -l gogo && mv gen/pb-gogo/github.com/hiett/lightning/thunderpb/* . && rm -rf gen"
