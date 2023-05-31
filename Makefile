.POSIX:

all: proxy-ns

proxy-ns: proxy-ns.c

clean:
	rm -f proxy-ns proxy-nsd@.service

proxy-nsd@.service: proxy-nsd@.service.in
	sed 's|@PREFIX@|$(PREFIX)|' $< > $@ || rm $@

install: proxy-ns proxy-nsd@.service
	mkdir -p $(DESTDIR)$(PREFIX)/bin
	install -m 755 proxy-ns $(DESTDIR)$(PREFIX)/bin/
	setcap cap_sys_admin=ep $(DESTDIR)$(PREFIX)/bin/proxy-ns

	install -m 755 proxy-nsd $(DESTDIR)$(PREFIX)/bin/

	mkdir -p $(DESTDIR)$(PREFIX)/lib/systemd/system
	install -m 644 proxy-nsd@.service $(DESTDIR)$(PREFIX)/lib/systemd/system/

	mkdir -p $(DESTDIR)/etc/proxy-nsd
	install -m 644 main.conf $(DESTDIR)/etc/proxy-nsd/

	mkdir -p $(DESTDIR)$(PREFIX)/share/doc/proxy-ns
	install -m 644 README.org $(DESTDIR)$(PREFIX)/share/doc/proxy-ns/

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/proxy-ns
	rm -f $(DESTDIR)$(PREFIX)/bin/proxy-nsd

	rm -f $(DESTDIR)$(PREFIX)/lib/systemd/system/proxy-nsd@.service

	rm -rf $(DESTDIR)/etc/proxy-nsd

	rm -rf $(DESTDIR)$(PREFIX)/share/doc/proxy-ns


.PHONY: all clean install uninstall
