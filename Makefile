prefix      = /usr/local
exec_prefix = $(prefix)
datadir     = $(prefix)/share
bindir      = $(exec_prefix)/bin
sysconfdir  = $(prefix)/etc

all: proxy-ns proxy-ns-doc

proxy-ns:
	go build -ldflags '-X main.SysConfDir=$(sysconfdir)' -trimpath -o $@

proxy-ns-doc:
	scdoc < doc/proxy-ns.1.scd > doc/proxy-ns.1
	scdoc < doc/proxy-ns.5.scd > doc/proxy-ns.5

install: proxy-ns
	install -Dm 755 proxy-ns $(DESTDIR)$(bindir)/proxy-ns
	setcap cap_sys_admin,cap_net_admin,cap_net_bind_service,cap_sys_chroot,cap_chown=ep $(DESTDIR)$(bindir)/proxy-ns

install-doc: proxy-ns-doc
	install -Dm 644 doc/proxy-ns.1 $(DESTDIR)$(datadir)/man/man1/proxy-ns.1
	install -Dm 644 doc/proxy-ns.5 $(DESTDIR)$(datadir)/man/man5/proxy-ns.5

install-config:
	install -Dm 644 config.json $(DESTDIR)$(sysconfdir)/proxy-ns/config.json

clean:
	rm -f proxy-ns
	rm -f doc/proxy-ns.1
	rm -f doc/proxy-ns.5

.PHONY: all proxy-ns proxy-ns-doc install install-doc install-config clean
