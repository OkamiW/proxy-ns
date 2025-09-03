prefix = /usr/local
exec_prefix = $(prefix)
datadir = $(prefix)/share
bindir = $(exec_prefix)/bin
sysconfdir = $(prefix)/etc

GO_DIR     = .
GO_SOURCES = $(shell find $(GO_DIR) -name '*.go') $(GO_DIR)/go.mod $(GO_DIR)/go.sum

proxy-ns: $(GO_SOURCES) Makefile
	CGO_ENABLED=0 go build -ldflags '-s -w -buildid= -X main.SysConfDir=$(sysconfdir)' -buildvcs=false -trimpath -o proxy-ns

test:
	GOARCH=386 CGO_ENABLED=0 go build -o /dev/null
	GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null
	GOARCH=arm CGO_ENABLED=0 go build -o /dev/null
	GOARCH=arm64 CGO_ENABLED=0 go build -o /dev/null
	GOARCH=riscv64 CGO_ENABLED=0 go build -o /dev/null

doc/proxy-ns.1: doc/proxy-ns.1.scd
	scdoc < doc/proxy-ns.1.scd > $@

doc/proxy-ns.5: doc/proxy-ns.5.scd
	scdoc < doc/proxy-ns.5.scd > $@

install: proxy-ns install-doc
	install -Dm 755 proxy-ns $(DESTDIR)$(bindir)/proxy-ns
	setcap cap_sys_admin,cap_net_admin,cap_net_bind_service,cap_sys_chroot,cap_chown=ep $(DESTDIR)$(bindir)/proxy-ns

install-doc: doc/proxy-ns.1 doc/proxy-ns.5
	install -Dm 644 doc/proxy-ns.1 $(DESTDIR)$(datadir)/man/man1/proxy-ns.1
	install -Dm 644 doc/proxy-ns.5 $(DESTDIR)$(datadir)/man/man5/proxy-ns.5

install-config:
	install -Dm 644 config.json $(DESTDIR)$(sysconfdir)/proxy-ns/config.json

clean:
	rm -f proxy-ns

.PHONY: test install install-doc install-config clean
