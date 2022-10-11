#define _GNU_SOURCE

#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <sched.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <unistd.h>

#define NETNS "/var/run/netns/proxy-ns"

#define RESOLV_FILE "/tmp/resolv.conf"
#define RESOLV_CONF "/etc/resolv.conf"

__attribute__ ((format (printf, 1, 0))) static void
warnv (const char *format, va_list args)
{
  fprintf (stderr, "proxy-ns: ");
  vfprintf (stderr, format, args);
  fprintf (stderr, "\n");
}

void
die (const char *format, ...)
{
  va_list args;

  va_start (args, format);
  warnv (format, args);
  va_end (args);

  exit (1);
}

void
die_with_error (const char *format, ...)
{
  va_list args;
  int errsv;

  errsv = errno;

  fprintf (stderr, "proxy-ns: ");

  va_start (args, format);
  vfprintf (stderr, format, args);
  va_end (args);

  fprintf (stderr, ": %s\n", strerror (errsv));

  exit (1);
}

int
main (int argc, char **argv)
{
  int netns;

  if (argc < 2 || strcmp (argv[1], "--help") == 0)
    {
      puts ("Usage: proxy-ns [command [argument ...]]");
      puts ("  More help in README file");
      exit (0);
    }

  argv++;
  argc--;

  netns = open (NETNS, O_RDONLY | O_CLOEXEC);
  if (netns < 0)
    {
      if (errno != ENOENT)
        {
          die_with_error ("Failed to open network namespace fd");
        }
      else
        {
          die (
            "Network namespace fd not found, is proxy-nsd running?");
        }
    }

  if (setns (netns, CLONE_NEWNET) != 0)
    die_with_error ("Failed to attach to network namespace");
  close (netns);

  if (unshare (CLONE_NEWNS) != 0)
    die_with_error ("Failed to unshare namespace");
  if (mount ("none", "/", NULL, MS_SILENT | MS_REC | MS_PRIVATE, NULL)
      != 0)
    die_with_error ("Failed to make root private");

  if (mount (RESOLV_FILE, RESOLV_CONF, NULL, MS_SILENT | MS_BIND,
             NULL)
      != 0)
    die_with_error ("Failed to mount bind resolv.conf");

  argv[argc] = (char *) NULL;
  if (execvp (argv[0], argv) != 0)
    die_with_error ("Failed to exec '%s'", argv[0]);

  return 0;
}
