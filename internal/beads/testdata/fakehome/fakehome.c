// LD_PRELOAD shim for the bd-77hbfy repro: rewrites the home directory that
// getpwuid_r reports for the current account to $BD_REPRO_FAKE_HOME, so a cgo
// os/user.Current() sees a service-account home such as /var/lib/<svc>.
#define _GNU_SOURCE
#include <dlfcn.h>
#include <pwd.h>
#include <stdlib.h>
#include <sys/types.h>

int getpwuid_r(uid_t uid, struct passwd *pwd, char *buf, size_t buflen, struct passwd **result) {
	static int (*real)(uid_t, struct passwd *, char *, size_t, struct passwd **);
	if (!real) {
		real = dlsym(RTLD_NEXT, "getpwuid_r");
	}
	int rc = real(uid, pwd, buf, buflen, result);
	const char *home = getenv("BD_REPRO_FAKE_HOME");
	if (rc == 0 && result && *result && home && *home) {
		pwd->pw_dir = (char *)home;
	}
	return rc;
}
