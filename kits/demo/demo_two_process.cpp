// Stage A proof: two separate processes (fork+exec, sharing only a namespace)
// exchange a flattened message over a port and share a buffer through an area.
#include <OS.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

// Minimal BMessage-style flat format: header, then name/size/data fields.
struct FlatHeader { uint32 magic; uint32 what; uint32 fieldCount; uint32 bytes; };
enum { kMagic = 0x4d534721 /* "MSG!" */, kWhatShare = 0x53484152 /* SHAR */, kWhatDone = 0x444f4e45 /* DONE */  };

struct Flat {
	char buf[1024]; size_t len;
	Flat(uint32 what) : len(sizeof(FlatHeader)) { FlatHeader h = { kMagic, what, 0, 0 }; memcpy(buf, &h, sizeof h); }
	void add(const char *name, const void *data, uint32 size) {
		uint8 nl = (uint8)strlen(name);
		buf[len++] = (char)nl; memcpy(buf + len, name, nl); len += nl;
		memcpy(buf + len, &size, 4); len += 4; memcpy(buf + len, data, size); len += size;
		FlatHeader *h = (FlatHeader *)buf; h->fieldCount++; h->bytes = (uint32)len;
	}
	// find a field; returns pointer into buf or NULL
	static const void *find(const char *msg, size_t n, const char *name, uint32 *size) {
		const FlatHeader *h = (const FlatHeader *)msg;
		if (n < sizeof *h || h->magic != kMagic) return NULL;
		size_t at = sizeof *h;
		for (uint32 i = 0; i < h->fieldCount; i++) {
			uint8 nl = (uint8)msg[at++]; bool hit = nl == strlen(name) && !memcmp(msg + at, name, nl);
			at += nl; uint32 s; memcpy(&s, msg + at, 4); at += 4;
			if (hit) { *size = s; return msg + at; }
			at += s;
		}
		return NULL;
	}
};

static int child(char **argv)
{
	port_id req = atoi(argv[2]), rep = atoi(argv[3]);
	char msg[1024]; int32 code;
	printf("[child  pid %d] waiting on port %d\n", getpid(), req);
	ssize_t n = read_port_etc(req, &code, msg, sizeof msg, B_RELATIVE_TIMEOUT, 10000000);
	if (n < 0) { printf("[child] read failed %d\n", (int)n); return 1; }
	const FlatHeader *h = (const FlatHeader *)msg;
	printf("[child  pid %d] got port msg code=0x%x, %zd bytes, flat what=0x%x fields=%u\n",
		getpid(), code, n, h->what, h->fieldCount);
	uint32 sz;
	const char *text = (const char *)Flat::find(msg, (size_t)n, "text", &sz);
	const int32 *aid = (const int32 *)Flat::find(msg, (size_t)n, "area", &sz);
	printf("[child  pid %d] field text=\"%s\" area_id=%d\n", getpid(), text, *aid);
	void *addr; area_id mine = clone_area("demo-clone", &addr, B_ANY_ADDRESS, B_READ_AREA | B_WRITE_AREA, *aid);
	if (mine < 0) { printf("[child] clone_area failed %d\n", (int)mine); return 2; }
	area_info ai; get_area_info(mine, &ai);
	printf("[child  pid %d] cloned area %d at %p (%zu bytes): \"%s\"\n", getpid(), mine, addr, ai.size, (char *)addr);
	snprintf((char *)addr + 256, 256, "reply written by child pid %d", getpid());
	Flat done(kWhatDone); int32 ok = 1; done.add("ok", &ok, 4);
	write_port(rep, 2, done.buf, done.len);
	delete_area(mine);
	return 0;
}

int main(int argc, char **argv)
{
	if (argc >= 4 && !strcmp(argv[1], "--child")) return child(argv);
	char ns[64]; snprintf(ns, sizeof ns, "demo-%d", getpid());
	setenv("AURORA_KITS_NS", ns, 1);
	port_id req = create_port(16, "demo-request"), rep = create_port(16, "demo-reply");
	void *addr; area_id a = create_area("demo-buffer", &addr, B_ANY_ADDRESS, 4096, B_NO_LOCK, B_READ_AREA | B_WRITE_AREA);
	snprintf((char *)addr, 256, "written by parent pid %d", getpid());
	printf("[parent pid %d] port req=%d rep=%d, area %d at %p: \"%s\"\n", getpid(), req, rep, a, addr, (char *)addr);

	pid_t pid = fork();
	if (pid == 0) {
		char s0[16], s1[16];
		snprintf(s0, 16, "%d", req); snprintf(s1, 16, "%d", rep);
		execl("/proc/self/exe", "demo_two_process", "--child", s0, s1, (char *)NULL);
		_exit(99);
	}
	Flat m(kWhatShare);
	m.add("text", "hello over a port", 18);
	int32 aid = a; m.add("area", &aid, 4);
	printf("[parent pid %d] sending flattened message (%zu bytes) with area_id %d\n", getpid(), m.len, a);
	write_port(req, 1, m.buf, m.len);

	char buf[1024]; int32 code;
	ssize_t n = read_port_etc(rep, &code, buf, sizeof buf, B_RELATIVE_TIMEOUT, 10000000);
	const FlatHeader *h = (const FlatHeader *)buf;
	printf("[parent pid %d] got reply code=%d flat what=0x%x (%zd bytes)\n", getpid(), code,
		h->what, n);
	printf("[parent pid %d] shared buffer now reads: \"%s\"\n", getpid(), (char *)addr + 256);
	int st; waitpid(pid, &st, 0);
	int good = n > 0 && WIFEXITED(st) && WEXITSTATUS(st) == 0 && strstr((char *)addr + 256, "child") != NULL;
	printf("[parent pid %d] child exit=%d -> %s\n", getpid(), WEXITSTATUS(st), good ? "PASS" : "FAIL");
	delete_area(a); delete_port(req); delete_port(rep);
	_kits_unlink_namespace();
	return good ? 0 : 1;
}
