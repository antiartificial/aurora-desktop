// Constructs a REAL Haiku BMessage (src/kits/app/Message.cpp), flattens, unflattens, round-trips.
#include <Message.h>
#include <String.h>
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <OS.h>

static int sFails = 0;
#define CHECK(c) do { if (!(c)) { printf("FAIL line %d: %s\n", __LINE__, #c); sFails++; } } while (0)

int main()
{
	BMessage m('test');
	CHECK(m.AddString("name", "aurora") == B_OK);
	CHECK(m.AddInt32("answer", 42) == B_OK);
	CHECK(m.AddInt32("answer", 43) == B_OK);
	CHECK(m.AddBool("flag", true) == B_OK);
	CHECK(m.AddDouble("pi", 3.14159) == B_OK);
	BMessage inner('inne');
	inner.AddString("k", "v");
	CHECK(m.AddMessage("nested", &inner) == B_OK);
	const char raw[] = {1, 2, 3, 4, 5};
	CHECK(m.AddData("blob", B_RAW_TYPE, raw, sizeof(raw)) == B_OK);

	ssize_t size = m.FlattenedSize();
	printf("FlattenedSize = %zd\n", size);
	char* buf = (char*)malloc(size);
	CHECK(m.Flatten(buf, size) == B_OK);
	printf("flat bytes (first 64 of %zd):", size);
	for (ssize_t i = 0; i < size && i < 64; i++) printf("%s%02x", i % 16 ? " " : "\n  ", (unsigned char)buf[i]);
	printf("\nmagic = %.4s\n", buf);   // Haiku: 'FOB2' on disk is little-endian bytes "1BOF"

	BMessage out;
	CHECK(out.Unflatten(buf) == B_OK);
	CHECK(out.what == 'test');
	const char* s; int32 i; bool b; double d;
	CHECK(out.FindString("name", &s) == B_OK && strcmp(s, "aurora") == 0);
	CHECK(out.FindInt32("answer", 0, &i) == B_OK && i == 42);
	CHECK(out.FindInt32("answer", 1, &i) == B_OK && i == 43);
	CHECK(out.FindBool("flag", &b) == B_OK && b);
	CHECK(out.FindDouble("pi", &d) == B_OK && d == 3.14159);
	BMessage in2;
	CHECK(out.FindMessage("nested", &in2) == B_OK && in2.what == 'inne');
	CHECK(in2.FindString("k", &s) == B_OK && strcmp(s, "v") == 0);
	const void* p; ssize_t n;
	CHECK(out.FindData("blob", B_RAW_TYPE, &p, &n) == B_OK && n == 5 && memcmp(p, raw, 5) == 0);
	CHECK(out.CountNames(B_ANY_TYPE) == 6);

	// second flatten of the copy must be byte-identical
	ssize_t size2 = out.FlattenedSize();
	char* buf2 = (char*)malloc(size2);
	CHECK(out.Flatten(buf2, size2) == B_OK);
	CHECK(size2 == size && memcmp(buf, buf2, size) == 0);
	// carry the real flat bytes through OUR port primitive and back
	port_id port = create_port(4, "haiku-compat");
	CHECK(port >= 0);
	CHECK(write_port(port, 'FLAT', buf, size) == B_OK);
	int32 code; char* rx = (char*)malloc(size);
	CHECK(read_port(port, &code, rx, size) == size && code == 'FLAT');
	BMessage viaPort;
	CHECK(viaPort.Unflatten(rx) == B_OK && viaPort.what == 'test' && viaPort.CountNames(B_ANY_TYPE) == 6);
	delete_port(port);
	printf("%s (%d failures)\n", sFails ? "FAILED" : "round trip OK", sFails);
	return sFails != 0;
}
