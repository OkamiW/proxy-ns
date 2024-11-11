prefix = /usr/local
exec_prefix = $(prefix)
bindir = $(exec_prefix)/bin
sysconfdir = $(prefix)/etc

GO_DIR     = .
GO_SOURCES = $(shell find $(GO_DIR) -name '*.go') $(GO_DIR)/go.mod $(GO_DIR)/go.sum

proxy-ns: $(GO_SOURCES) Makefile
	CGO_ENABLED=0 go build -ldflags '-s -w -buildid= -X main.SysConfDir=$(sysconfdir)' -buildvcs=false -trimpath -o proxy-ns ./cmd/proxy-ns

install: proxy-ns
	install -Dm 755 proxy-ns $(DESTDIR)$(bindir)/proxy-ns
	setcap cap_net_bind_service,cap_fowner,cap_chown,cap_sys_chroot,cap_sys_admin,cap_net_admin=ep $(DESTDIR)$(bindir)/proxy-ns

install-config:
	install -Dm 644 config.json $(DESTDIR)$(sysconfdir)/proxy-ns/config.json

clean:
	rm -f proxy-ns

.PHONY: GOTOUCH install clean
