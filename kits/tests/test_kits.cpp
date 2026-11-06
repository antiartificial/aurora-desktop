// Primitives-layer tests. Written in C++ on purpose: Haiku's kits are C++.
#include <OS.h>
#include <signal.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

static int failures, checks;
#define CHECK(c) do { checks++; if (!(c)) { failures++; \
	printf("  FAIL %s:%d: %s\n", __FILE__, __LINE__, #c); } } while (0)
#define CHECK_EQ(a, b) do { long long _a = (a), _b = (b); checks++; if (_a != _b) { \
	failures++; printf("  FAIL %s:%d: %s == %s (%lld vs %lld)\n", __FILE__, __LINE__, \
	#a, #b, _a, _b); } } while (0)
#define RUN(t) do { printf("[ RUN ] %s\n", #t); int f0 = failures; t(); \
	printf("[ %s ] %s\n", failures == f0 ? " OK " : "FAIL", #t); } while (0)

// ---- child mode: a separate process image (fork+exec), sharing only the namespace
static int child_main(char **argv)
{
	port_id in = atoi(argv[2]), out = atoi(argv[3]);
	area_id src = atoi(argv[4]);
	char buf[256]; int32 code;
	ssize_t n = read_port_etc(in, &code, buf, sizeof buf, B_RELATIVE_TIMEOUT, 5000000);
	if (n < 0) return 10;
	void *addr = NULL;
	area_id mine = clone_area("clone", &addr, B_ANY_ADDRESS, B_READ_AREA | B_WRITE_AREA, src);
	if (mine < 0) return 11;
	uint32 sum = 0;
	for (size_t i = 0; i < 8192; i++) sum += ((unsigned char *)addr)[i];
	((char *)addr)[0] = 'C'; // child's write must be visible to the parent
	int32 reply[3] = { (int32)n, (int32)sum, code };
	if (write_port(out, 0x52455059, reply, sizeof reply) != B_OK) return 12;
	return strcmp(buf, "hello from parent") == 0 ? 0 : 13;
}

static void test_port_roundtrip_local()
{
	port_id p = create_port(4, "local");
	CHECK(p >= 0);
	int32 v = 0x1234, code = 0, got = 0;
	CHECK_EQ(write_port(p, 77, &v, sizeof v), B_OK);
	CHECK_EQ(read_port(p, &code, &got, sizeof got), (long long)sizeof got);
	CHECK_EQ(code, 77); CHECK_EQ(got, 0x1234);
	CHECK_EQ(write_port(p, 5, NULL, 0), B_OK); // empty payload is legal
	CHECK_EQ(read_port(p, &code, NULL, 0), 0); CHECK_EQ(code, 5);
	// ring wraparound: many variable-sized messages through a small queue
	char out[3000], in[3000];
	for (int i = 0; i < 500; i++) {
		size_t len = (i * 37) % 3000;
		memset(out, i & 0xff, len);
		CHECK_EQ(write_port(p, i, out, len), B_OK);
		CHECK_EQ(read_port(p, &code, in, sizeof in), (long long)len);
		CHECK(code == i && memcmp(in, out, len) == 0);
	}
	CHECK_EQ(delete_port(p), B_OK);
	CHECK_EQ(delete_port(p), B_BAD_PORT_ID);
}

static void test_port_two_processes_fork()
{
	port_id req = create_port(8, "req"), rep = create_port(8, "rep");
	pid_t pid = fork();
	if (pid == 0) { // child: echo with code+1
		char b[64]; int32 c;
		ssize_t n = read_port(req, &c, b, sizeof b);
		if (n < 0) _exit(1);
		_exit(write_port(rep, c + 1, b, (size_t)n) == B_OK ? 0 : 2);
	}
	const char msg[] = "ping across processes";
	CHECK_EQ(write_port(req, 41, msg, sizeof msg), B_OK);
	char b[64]; int32 c = 0;
	ssize_t n = read_port_etc(rep, &c, b, sizeof b, B_RELATIVE_TIMEOUT, 5000000);
	CHECK_EQ(n, (long long)sizeof msg); CHECK_EQ(c, 42); CHECK(!strcmp(b, msg));
	int st; waitpid(pid, &st, 0);
	CHECK(WIFEXITED(st) && WEXITSTATUS(st) == 0);
	delete_port(req); delete_port(rep);
}

static void test_exec_child_port_and_area()
{
	port_id in = create_port(4, "x-in"), out = create_port(4, "x-out");
	void *addr; area_id a = create_area("shared", &addr, B_ANY_ADDRESS, 8192, B_NO_LOCK,
		B_READ_AREA | B_WRITE_AREA);
	CHECK(a >= 0);
	uint32 sum = 0;
	for (size_t i = 0; i < 8192; i++) { ((unsigned char *)addr)[i] = (unsigned char)(i * 7 + 3); sum += (unsigned char)(i * 7 + 3); }
	pid_t pid = fork();
	if (pid == 0) {
		char s[3][16]; snprintf(s[0], 16, "%d", in); snprintf(s[1], 16, "%d", out); snprintf(s[2], 16, "%d", a);
		execl("/proc/self/exe", "test_kits", "--child", s[0], s[1], s[2], (char *)NULL);
		_exit(99);
	}
	const char msg[] = "hello from parent";
	CHECK_EQ(write_port(in, 9, msg, sizeof msg - 1), B_OK);
	int32 r[3] = {0}, code = 0;
	CHECK_EQ(read_port_etc(out, &code, r, sizeof r, B_RELATIVE_TIMEOUT, 5000000), (long long)sizeof r);
	CHECK_EQ(code, 0x52455059); CHECK_EQ(r[0], (int)sizeof msg - 1);
	CHECK_EQ((uint32)r[1], sum);   // identical contents seen by the other process
	CHECK_EQ(r[2], 9);
	int st; waitpid(pid, &st, 0);
	CHECK(WIFEXITED(st) && WEXITSTATUS(st) == 0);
	CHECK_EQ(((char *)addr)[0], 'C'); // and the child's write is visible here
	area_info ai; CHECK_EQ(get_area_info(a, &ai), B_OK);
	CHECK(ai.size == 8192 && ai.address == addr && ai.team == getpid());
	delete_area(a); delete_port(in); delete_port(out);
}

struct WriteCtx { port_id p; int32 code; bigtime_t delay; };
static status_t delayed_writer(void *d)
{
	WriteCtx *c = (WriteCtx *)d;
	snooze(c->delay);
	return write_port(c->p, c->code, "x", 1);
}

static void test_port_blocking()
{
	port_id p = create_port(1, "blk");
	WriteCtx c = { p, 11, 150000 };
	thread_id t = spawn_thread(delayed_writer, "w", B_NORMAL_PRIORITY, &c);
	resume_thread(t);
	bigtime_t t0 = system_time(); int32 code = 0; char b;
	CHECK_EQ(read_port(p, &code, &b, 1), 1);        // blocks until writer writes
	bigtime_t dt = system_time() - t0;
	CHECK(dt >= 120000 && dt < 2000000); CHECK_EQ(code, 11);
	status_t rv; wait_for_thread(t, &rv);

	// capacity 1: second write blocks until a reader drains
	CHECK_EQ(write_port(p, 1, "a", 1), B_OK);
	struct R { port_id p; } r = { p };
	thread_id rt = spawn_thread([](void *d) -> status_t { snooze(150000); int32 c; char x;
		return (status_t)read_port(((R *)d)->p, &c, &x, 1); }, "r", B_NORMAL_PRIORITY, &r);
	resume_thread(rt);
	t0 = system_time();
	CHECK_EQ(write_port(p, 2, "b", 1), B_OK);        // full: must wait for the reader
	dt = system_time() - t0;
	CHECK(dt >= 120000 && dt < 2000000);
	wait_for_thread(rt, &rv);
	CHECK_EQ(port_count(p), 1);
	delete_port(p);
}

static void test_port_timeouts()
{
	port_id p = create_port(1, "to"); int32 code; char b;
	bigtime_t t0 = system_time();
	CHECK_EQ(read_port_etc(p, &code, &b, 1, B_RELATIVE_TIMEOUT, 100000), B_TIMED_OUT);
	CHECK(system_time() - t0 >= 90000);
	CHECK_EQ(read_port_etc(p, &code, &b, 1, B_RELATIVE_TIMEOUT, 0), B_WOULD_BLOCK);
	CHECK_EQ(read_port_etc(p, &code, &b, 1, B_ABSOLUTE_TIMEOUT, system_time() + 80000), B_TIMED_OUT);
	CHECK_EQ(write_port(p, 1, "a", 1), B_OK);
	CHECK_EQ(write_port_etc(p, 2, "b", 1, B_RELATIVE_TIMEOUT, 100000), B_TIMED_OUT);
	CHECK_EQ(write_port_etc(p, 2, "b", 1, B_RELATIVE_TIMEOUT, 0), B_WOULD_BLOCK);
	CHECK_EQ(port_count(p), 1);
	delete_port(p);
}

static status_t blocked_reader(void *d)
{
	int32 code; char b;
	return (status_t)read_port(*(port_id *)d, &code, &b, 1);
}
static void test_port_delete_and_close_wake_reader()
{
	for (int mode = 0; mode < 2; mode++) {
		port_id p = create_port(2, "del");
		thread_id t = spawn_thread(blocked_reader, "rd", B_NORMAL_PRIORITY, &p);
		resume_thread(t);
		snooze(100000);
		if (mode == 0) delete_port(p); else close_port(p);
		status_t rv = 0; wait_for_thread(t, &rv);
		CHECK_EQ(rv, B_BAD_PORT_ID);
		if (mode) delete_port(p);
	}
	// a closed port still hands out queued messages, then reports bad id
	port_id p = create_port(2, "cl"); int32 c; char b;
	write_port(p, 3, "z", 1); close_port(p);
	CHECK_EQ(write_port(p, 4, "z", 1), B_BAD_PORT_ID);
	CHECK_EQ(read_port(p, &c, &b, 1), 1);
	CHECK_EQ(read_port(p, &c, &b, 1), B_BAD_PORT_ID);
	delete_port(p);
}

static void test_port_count_and_info()
{
	port_id p = create_port(8, "cnt");
	CHECK_EQ(port_count(p), 0);
	for (int i = 0; i < 5; i++) write_port(p, i, &i, sizeof i);
	CHECK_EQ(port_count(p), 5);
	int32 c, v; read_port(p, &c, &v, sizeof v); read_port(p, &c, &v, sizeof v);
	CHECK_EQ(port_count(p), 3);
	port_info pi; CHECK_EQ(get_port_info(p, &pi), B_OK);
	CHECK(pi.capacity == 8 && pi.queue_count == 3 && pi.total_count == 2 && !strcmp(pi.name, "cnt"));
	CHECK_EQ(port_buffer_size(p), 4);
	CHECK_EQ(set_port_owner(p, getppid()), B_OK);
	CHECK_EQ(find_port("cnt"), p);
	CHECK_EQ(port_count(9999999), B_BAD_PORT_ID);
	delete_port(p);
	CHECK_EQ(port_count(p), B_BAD_PORT_ID);
}

static void test_area_protection()
{
	void *addr; area_id a = create_area("prot", &addr, B_ANY_ADDRESS, 4096, B_NO_LOCK, B_READ_AREA | B_WRITE_AREA);
	strcpy((char *)addr, "data");
	area_info ai; get_area_info(a, &ai);
	CHECK_EQ(ai.protection, B_READ_AREA | B_WRITE_AREA);
	CHECK_EQ(set_area_protection(a, B_READ_AREA), B_OK);
	get_area_info(a, &ai); CHECK_EQ(ai.protection, B_READ_AREA);
	CHECK(!strcmp((char *)addr, "data")); // still readable
	pid_t pid = fork();                    // a write must now fault
	if (pid == 0) { *(volatile char *)addr = 'X'; _exit(0); }
	int st; waitpid(pid, &st, 0);
	CHECK(WIFSIGNALED(st) && WTERMSIG(st) == SIGSEGV);
	CHECK_EQ(set_area_protection(a, B_READ_AREA | B_WRITE_AREA), B_OK);
	((char *)addr)[0] = 'Y';              // writable again
	CHECK_EQ(((char *)addr)[0], 'Y');
	CHECK_EQ(set_area_protection(12345678, B_READ_AREA), B_BAD_VALUE);
	// the fd export path (what the bus's SCM_RIGHTS uses): sealed memfd -> new mapping
	int fd = area_export_fd(a); void *a2; area_id b = clone_area_fd("viafd", &a2, B_ANY_ADDRESS, B_READ_AREA, fd);
	CHECK(b >= 0 && a2 != addr && ((char *)a2)[0] == 'Y');
	close(fd);
	CHECK_EQ(delete_area(b), B_OK); CHECK_EQ(delete_area(a), B_OK);
	CHECK_EQ(delete_area(a), B_BAD_VALUE);
}

static status_t sem_acquirer(void *d) { return acquire_sem_etc(*(sem_id *)d, 2, 0, 0); }
static void test_semaphores()
{
	sem_id s = create_sem(2, "s"); int32 n;
	CHECK(s >= 0);
	CHECK_EQ(acquire_sem(s), B_OK); CHECK_EQ(acquire_sem(s), B_OK);
	get_sem_count(s, &n); CHECK_EQ(n, 0);
	CHECK_EQ(acquire_sem_etc(s, 1, B_RELATIVE_TIMEOUT, 0), B_WOULD_BLOCK);
	bigtime_t t0 = system_time();
	CHECK_EQ(acquire_sem_etc(s, 1, B_RELATIVE_TIMEOUT, 100000), B_TIMED_OUT);
	CHECK(system_time() - t0 >= 90000);
	CHECK_EQ(release_sem_etc(s, 3, 0), B_OK);
	get_sem_count(s, &n); CHECK_EQ(n, 3);
	CHECK_EQ(acquire_sem_etc(s, 3, 0, 0), B_OK);       // counted acquire is atomic
	// a thread blocked in acquire(2) needs both units
	thread_id t = spawn_thread(sem_acquirer, "acq", B_NORMAL_PRIORITY, &s); resume_thread(t);
	snooze(60000); release_sem(s); snooze(60000);
	get_sem_count(s, &n); CHECK_EQ(n, 1);               // one unit is not enough
	release_sem(s);
	status_t rv = -1; wait_for_thread(t, &rv); CHECK_EQ(rv, B_OK);
	// deleting wakes waiters
	sem_id d = create_sem(0, "d");
	thread_id t2 = spawn_thread([](void *p) -> status_t { return acquire_sem(*(sem_id *)p); }, "a2", B_NORMAL_PRIORITY, &d);
	resume_thread(t2); snooze(60000); delete_sem(d);
	wait_for_thread(t2, &rv); CHECK_EQ(rv, B_BAD_SEM_ID);
	CHECK_EQ(acquire_sem(d), B_BAD_SEM_ID);
	CHECK_EQ(create_sem(-1, "bad"), B_BAD_VALUE);
	delete_sem(s);
}

static volatile int ran;
static thread_id seen;
static status_t thr(void *) { seen = find_thread(NULL); ran = 1; return 7; }
static void test_threads()
{
	thread_id me = find_thread(NULL);
	CHECK_EQ(find_thread(NULL), me);
	thread_id t = spawn_thread(thr, "worker", B_NORMAL_PRIORITY, NULL);
	CHECK(t >= 0);
	snooze(150000);
	CHECK_EQ(ran, 0);                  // born suspended
	CHECK_EQ(find_thread("worker"), t);
	CHECK_EQ(resume_thread(t), B_OK);
	status_t rv = 0; CHECK_EQ(wait_for_thread(t, &rv), B_OK);
	CHECK_EQ(ran, 1); CHECK_EQ(rv, 7);
	CHECK_EQ(seen, t);                 // the id from spawn matches find_thread inside
	CHECK(seen != me);
	CHECK_EQ(resume_thread(99999999), B_BAD_THREAD_ID);
}

int main(int argc, char **argv)
{
	if (argc >= 5 && !strcmp(argv[1], "--child")) return child_main(argv);
	char ns[64]; snprintf(ns, sizeof ns, "test-%d", getpid());
	setenv("AURORA_KITS_NS", ns, 1);   // private namespace; children inherit it
	RUN(test_port_roundtrip_local);
	RUN(test_port_two_processes_fork);
	RUN(test_exec_child_port_and_area);
	RUN(test_port_blocking);
	RUN(test_port_timeouts);
	RUN(test_port_delete_and_close_wake_reader);
	RUN(test_port_count_and_info);
	RUN(test_area_protection);
	RUN(test_semaphores);
	RUN(test_threads);
	_kits_unlink_namespace();
	printf("%d checks, %d failures\n", checks, failures);
	return failures ? 1 : 0;
}
