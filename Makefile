PREFIX = /usr/local

GO_DIR     = .
GO_SOURCES = $(shell find $(GO_DIR) -name '*.go') $(GO_DIR)/go.mod $(GO_DIR)/go.sum

cmds/proxy-ns/proxy-ns: $(GO_SOURCES) Makefile
	CGO_ENABLED=0 go build -C cmds/proxy-ns -ldflags '-s -w -buildid=' -buildvcs=false -trimpath -o proxy-ns

clean:
	rm -f cmds/proxy-ns/proxy-ns

install: cmds/proxy-ns/proxy-ns
	install -Dm 755 cmds/proxy-ns/proxy-ns $(DESTDIR)$(PREFIX)/bin/proxy-ns
	setcap cap_net_bind_service,cap_fowner,cap_chown,cap_sys_chroot,cap_sys_admin,cap_net_admin=ep $(DESTDIR)$(PREFIX)/bin/proxy-ns

.PHONY: GOTOUCH install clean
