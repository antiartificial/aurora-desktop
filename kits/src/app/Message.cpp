// BMessage implementation (original). Flat format, little-endian host order:
//   u32 magic 'KMS1', u32 what, u32 fieldCount,
//   per field: u32 type, u16 nameLen, name bytes, u32 itemCount,
//              per item: u32 size, bytes.
#include <Message.h>
#include <stdio.h>
#include <string.h>

static const uint32 kFlatMagic = 'KMS1';

BMessage::BMessage() : what(0) {}
BMessage::BMessage(uint32 w) : what(w) {}
BMessage::BMessage(const BMessage &o) { *this = o; }
BMessage::~BMessage() {}

BMessage &BMessage::operator=(const BMessage &o)
{
	if (this != &o) {
		what = o.what; fFields = o.fFields; fReplyPort = o.fReplyPort;
		fReplyToken = o.fReplyToken; fWantsReply = o.fWantsReply; fReplied = o.fReplied;
		fDelivered = o.fDelivered; fSenderTeam = o.fSenderTeam;
	}
	return *this;
}

bool BMessage::Equals(const BMessage &o) const
{
	if (what != o.what || fFields.size() != o.fFields.size()) return false;
	for (size_t i = 0; i < fFields.size(); i++) {
		const Field &a = fFields[i], &b = o.fFields[i];
		if (a.name != b.name || a.type != b.type || a.items != b.items) return false;
	}
	return true;
}

bool BMessage::IsEmpty() const { return fFields.empty(); }

int32 BMessage::CountNames(type_code type) const
{
	int32 n = 0;
	for (const Field &f : fFields) if (type == B_ANY_TYPE || f.type == type) n++;
	return n;
}

const BMessage::Field *BMessage::find(const char *name) const
{
	if (!name) return NULL;
	for (const Field &f : fFields) if (f.name == name) return &f;
	return NULL;
}

status_t BMessage::GetInfo(const char *name, type_code *type, int32 *count) const
{
	const Field *f = find(name);
	if (!f) return B_NAME_NOT_FOUND;
	if (type) *type = f->type;
	if (count) *count = (int32)f->items.size();
	return B_OK;
}

status_t BMessage::GetInfo(type_code want, int32 index, char **name, type_code *type, int32 *count) const
{
	if (index < 0) return B_BAD_INDEX;
	for (const Field &f : fFields) {
		if (want != B_ANY_TYPE && f.type != want) continue;
		if (index-- > 0) continue;
		if (name) *name = const_cast<char *>(f.name.c_str());
		if (type) *type = f.type;
		if (count) *count = (int32)f.items.size();
		return B_OK;
	}
	return B_BAD_INDEX;
}

status_t BMessage::RemoveName(const char *name)
{
	for (size_t i = 0; name && i < fFields.size(); i++)
		if (fFields[i].name == name) { fFields.erase(fFields.begin() + i); return B_OK; }
	return B_NAME_NOT_FOUND;
}

status_t BMessage::MakeEmpty() { fFields.clear(); return B_OK; }

BMessage::Field *BMessage::findOrAdd(const char *name, type_code type, status_t *err)
{
	*err = B_OK;
	if (!name || strlen(name) > 0xffff) { *err = B_BAD_VALUE; return NULL; }
	for (Field &f : fFields) if (f.name == name) {
		if (f.type != type) { *err = B_BAD_TYPE; return NULL; }
		return &f;
	}
	fFields.push_back(Field{name, type, {}});
	return &fFields.back();
}

status_t BMessage::addRaw(const char *name, type_code type, const void *p, size_t n)
{
	if (!p && n) return B_BAD_VALUE;
	status_t err; Field *f = findOrAdd(name, type, &err);
	if (!f) return err;
	try { f->items.emplace_back((const char *)p, n); } catch (...) { return B_NO_MEMORY; }
	return B_OK;
}

status_t BMessage::findRaw(const char *name, type_code type, int32 index, const std::string **out) const
{
	const Field *f = find(name);
	if (!f) return B_NAME_NOT_FOUND;
	if (type != B_ANY_TYPE && f->type != type) return B_BAD_TYPE;
	if (index < 0 || (size_t)index >= f->items.size()) return B_BAD_INDEX;
	*out = &f->items[index];
	return B_OK;
}

template <class T> status_t BMessage::findPod(const char *name, type_code type, int32 index, T *v) const
{
	const std::string *s; status_t r = findRaw(name, type, index, &s);
	if (r != B_OK) return r;
	if (s->size() != sizeof(T)) return B_BAD_DATA;
	if (v) memcpy(v, s->data(), sizeof(T));
	return B_OK;
}

status_t BMessage::AddInt8(const char *n, int8 v) { return addPod(n, B_INT8_TYPE, v); }
status_t BMessage::AddInt16(const char *n, int16 v) { return addPod(n, B_INT16_TYPE, v); }
status_t BMessage::AddInt32(const char *n, int32 v) { return addPod(n, B_INT32_TYPE, v); }
status_t BMessage::AddInt64(const char *n, int64 v) { return addPod(n, B_INT64_TYPE, v); }
status_t BMessage::AddBool(const char *n, bool v) { return addPod(n, B_BOOL_TYPE, (uint8)(v ? 1 : 0)); }
status_t BMessage::AddFloat(const char *n, float v) { return addPod(n, B_FLOAT_TYPE, v); }
status_t BMessage::AddDouble(const char *n, double v) { return addPod(n, B_DOUBLE_TYPE, v); }
status_t BMessage::AddString(const char *n, const char *v)
{
	return v ? addRaw(n, B_STRING_TYPE, v, strlen(v) + 1) : B_BAD_VALUE;
}
status_t BMessage::AddData(const char *n, type_code type, const void *d, ssize_t size)
{
	return size < 0 ? B_BAD_VALUE : addRaw(n, type, d, (size_t)size);
}
status_t BMessage::AddMessage(const char *n, const BMessage *m)
{
	if (!m) return B_BAD_VALUE;
	std::string buf((size_t)m->FlattenedSize(), '\0');
	status_t r = m->Flatten(&buf[0], (ssize_t)buf.size());
	return r != B_OK ? r : addRaw(n, B_MESSAGE_TYPE, buf.data(), buf.size());
}

status_t BMessage::FindInt8(const char *n, int32 i, int8 *v) const { return findPod(n, B_INT8_TYPE, i, v); }
status_t BMessage::FindInt16(const char *n, int32 i, int16 *v) const { return findPod(n, B_INT16_TYPE, i, v); }
status_t BMessage::FindInt32(const char *n, int32 i, int32 *v) const { return findPod(n, B_INT32_TYPE, i, v); }
status_t BMessage::FindInt64(const char *n, int32 i, int64 *v) const { return findPod(n, B_INT64_TYPE, i, v); }
status_t BMessage::FindFloat(const char *n, int32 i, float *v) const { return findPod(n, B_FLOAT_TYPE, i, v); }
status_t BMessage::FindDouble(const char *n, int32 i, double *v) const { return findPod(n, B_DOUBLE_TYPE, i, v); }
status_t BMessage::FindBool(const char *n, int32 i, bool *v) const
{
	uint8 b; status_t r = findPod(n, B_BOOL_TYPE, i, &b);
	if (r == B_OK && v) *v = b != 0;
	return r;
}
status_t BMessage::FindString(const char *n, int32 i, const char **v) const
{
	const std::string *s; status_t r = findRaw(n, B_STRING_TYPE, i, &s);
	if (r != B_OK) return r;
	if (v) *v = s->c_str();
	return B_OK;
}
status_t BMessage::FindData(const char *n, type_code t, int32 i, const void **d, ssize_t *size) const
{
	const std::string *s; status_t r = findRaw(n, t, i, &s);
	if (r != B_OK) return r;
	if (d) *d = s->data();
	if (size) *size = (ssize_t)s->size();
	return B_OK;
}
status_t BMessage::FindMessage(const char *n, int32 i, BMessage *m) const
{
	const std::string *s; status_t r = findRaw(n, B_MESSAGE_TYPE, i, &s);
	return r != B_OK ? r : (m ? m->Unflatten(s->data(), (ssize_t)s->size()) : B_BAD_VALUE);
}

ssize_t BMessage::FlattenedSize() const
{
	size_t n = 12;
	for (const Field &f : fFields) {
		n += 4 + 2 + f.name.size() + 4;
		for (const std::string &it : f.items) n += 4 + it.size();
	}
	return (ssize_t)n;
}

namespace {
struct Writer {
	char *p; void put(const void *d, size_t n) { memcpy(p, d, n); p += n; }
	void u32(uint32 v) { put(&v, 4); }
};
struct Reader {
	const char *p, *end; bool ok = true;
	bool take(void *d, size_t n) {
		if ((size_t)(end - p) < n) return ok = false;
		memcpy(d, p, n); p += n; return true;
	}
	uint32 u32() { uint32 v = 0; take(&v, 4); return v; }
};
}

status_t BMessage::Flatten(char *buffer, ssize_t size) const
{
	if (!buffer || size < FlattenedSize()) return B_BAD_VALUE;
	Writer w{buffer};
	w.u32(kFlatMagic); w.u32(what); w.u32((uint32)fFields.size());
	for (const Field &f : fFields) {
		w.u32(f.type);
		uint16 nl = (uint16)f.name.size(); w.put(&nl, 2); w.put(f.name.data(), nl);
		w.u32((uint32)f.items.size());
		for (const std::string &it : f.items) { w.u32((uint32)it.size()); w.put(it.data(), it.size()); }
	}
	return B_OK;
}

status_t BMessage::Unflatten(const char *buffer, ssize_t size)
{
	if (!buffer || size < 12) return B_BAD_VALUE;
	Reader r{buffer, buffer + size};
	if (r.u32() != kFlatMagic) return B_BAD_DATA;
	BMessage tmp; tmp.what = r.u32();
	uint32 nf = r.u32();
	for (uint32 i = 0; i < nf && r.ok; i++) {
		Field f; f.type = r.u32();
		uint16 nl = 0; r.take(&nl, 2);
		if (!r.ok || (size_t)(r.end - r.p) < nl) return B_BAD_DATA;
		f.name.assign(r.p, nl); r.p += nl;
		uint32 ni = r.u32();
		for (uint32 j = 0; j < ni && r.ok; j++) {
			uint32 sz = r.u32();
			if (!r.ok || (size_t)(r.end - r.p) < sz) return B_BAD_DATA;
			f.items.emplace_back(r.p, sz); r.p += sz;
		}
		if (!r.ok) return B_BAD_DATA;
		for (const Field &g : tmp.fFields) if (g.name == f.name) return B_BAD_DATA;
		tmp.fFields.push_back(std::move(f));
	}
	if (!r.ok) return B_BAD_DATA;
	tmp.fReplyPort = fReplyPort; tmp.fReplyToken = fReplyToken; tmp.fWantsReply = fWantsReply;
	tmp.fReplied = fReplied; tmp.fDelivered = fDelivered; tmp.fSenderTeam = fSenderTeam;
	*this = tmp;
	return B_OK;
}

void BMessage::PrintToStream() const
{
	printf("BMessage(what = '%c%c%c%c' (0x%x))\n", (char)(what >> 24), (char)(what >> 16),
		(char)(what >> 8), (char)what, what);
	for (const Field &f : fFields) {
		printf("    %s: type 0x%08x, %zu item(s)", f.name.c_str(), f.type, f.items.size());
		if (f.type == B_STRING_TYPE) printf(", \"%s\"", f.items[0].c_str());
		if (f.type == B_INT32_TYPE && f.items[0].size() == 4) { int32 v; memcpy(&v, f.items[0].data(), 4); printf(", %d", v); }
		printf("\n");
	}
}
