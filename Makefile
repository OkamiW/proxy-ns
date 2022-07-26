P       = proxy-ns
SRC     = proxy-ns.c

UTILS   = proxy-nsd
SERVICE = proxy-nsd.service
CONFIG  = proxy-nsd.conf

CFLAGS  = -ansi -pedantic -Wall -Wextra -O3 -g
PREFIX  = /usr/local

$(P): $(SRC)
	$(CC) $(CFLAGS) $< -o $@

install: $(P)
	mkdir -p $(DESTDIR)$(PREFIX)/bin
	install -m 755 $(P) $(DESTDIR)$(PREFIX)/bin
	install -m 755 $(UTILS) $(DESTDIR)$(PREFIX)/bin

	mkdir -p $(DESTDIR)$(PREFIX)/lib/systemd/system
	install -m 644 $(SERVICE) $(DESTDIR)$(PREFIX)/lib/systemd/system

	mkdir -p $(DESTDIR)/etc/
	install -m 644 $(CONFIG) $(DESTDIR)/etc/
