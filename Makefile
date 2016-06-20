TARGET  ?= build

GO_VERSION ?= 1.6
DOCKER ?= docker
DOCKERTAG ?= latest
DOCKERREPO ?= localhost
APPNAME = flowlog-exporter
WORKDIR ?= $(CURDIR)
BENCHTIME ?= 1s

test:
	go test -run=. -bench=. -benchmem -benchtime $(BENCHTIME)

build:
	$(DOCKER) run --rm \
		--privileged \
		-v $(WORKDIR):/src \
		-e CGO_ENABLED=0 \
		-e LDFLAGS='-s -extldflags -static' \
		-v /var/run/docker.sock:/var/run/docker.sock \
		centurylink/golang-builder \
		$(DOCKERREPO)/$(APPNAME):$(DOCKERTAG)
	$(DOCKER) tag -f $(DOCKERREPO)/$(APPNAME):$(DOCKERTAG) $(DOCKERREPO)/$(APPNAME):latest

push:
	$(DOCKER) push $(DOCKERREPO)/$(APPNAME):$(DOCKERTAG)
	$(DOCKER) push $(DOCKERREPO)/$(APPNAME):latest

clean:
	rm -f $(APPNAME)