// JSON <-> BMessage. The mapping is documented in kits/README.md "Bus bridge".
#include <BusBridge.h>
#include <limits.h>
#include <string.h>

static const char *kArrays = "bus:arrays";

void BusMarkArray(BMessage *m, const char *name) { m->AddString(kArrays, name); }

static bool fits32(int64_t v) { return v >= INT_MIN && v <= INT_MAX; }

static status_t addScalar(BMessage *m, const char *name, const Json &v, std::string *err);

// A JSON array becomes one repeated field. BMessage has one type per name, so the items
// must agree: all bool, all string, all object, or all numbers (promoted to the widest).
static status_t addArray(BMessage *m, const char *name, const Json &a, std::string *err)
{
	const std::vector<Json> &it = a.items();
	if (it.empty()) return B_OK;                        // lossy: no empty fields in a BMessage
	bool allInt = true, allNum = true, big = false;
	for (const Json &e : it) {
		if (e.type() != Json::Int && e.type() != Json::Double) allNum = false;
		if (e.type() != Json::Int) allInt = false;
		else if (!fits32(e.asInt())) big = true;
		if (e.type() != it[0].type() && !(allNum && (it[0].type() == Json::Int || it[0].type() == Json::Double))) {
			*err = std::string("field \"") + name + "\": array items have mixed types";
			return B_BAD_VALUE;
		}
	}
	if (it[0].type() == Json::Null || it[0].type() == Json::Array) {
		*err = std::string("field \"") + name + "\": null or nested array items are not representable";
		return B_BAD_VALUE;
	}
	status_t r = B_OK;
	for (const Json &e : it) {
		if (allNum && !allInt) r = m->AddDouble(name, e.asDouble());
		else if (allNum && big) r = m->AddInt64(name, e.asInt());
		else if (allNum) r = m->AddInt32(name, (int32)e.asInt());
		else r = addScalar(m, name, e, err);
		if (r != B_OK) return r;
	}
	return B_OK;
}

static status_t addScalar(BMessage *m, const char *name, const Json &v, std::string *err)
{
	switch (v.type()) {
	case Json::Null: return B_OK;                       // lossy: BMessage has no null
	case Json::Bool: return m->AddBool(name, v.asBool());
	case Json::Int: return fits32(v.asInt()) ? m->AddInt32(name, (int32)v.asInt()) : m->AddInt64(name, v.asInt());
	case Json::Double: return m->AddDouble(name, v.asDouble());
	case Json::String: return m->AddString(name, v.asString().c_str());   // lossy: embedded NUL truncates
	case Json::Array: return addArray(m, name, v, err);
	case Json::Object: {
		BMessage sub;
		status_t r = BusJsonToMessage(v, &sub, err);
		return r != B_OK ? r : m->AddMessage(name, &sub);
	}
	}
	return B_BAD_VALUE;
}

status_t BusJsonToMessage(const Json &payload, BMessage *out, std::string *err)
{
	std::string scratch; if (!err) err = &scratch;
	if (payload.isNull()) return B_OK;
	if (!payload.isObject()) {                          // a bare value is carried as field "value"
		BMessage tmp;
		status_t r = addScalar(&tmp, "value", payload, err);
		if (r == B_OK) { tmp.what = out->what; *out = tmp; }
		return r;
	}
	BMessage tmp;
	for (auto &mem : payload.members()) {
		if (mem.first.empty()) { *err = "empty field name"; return B_BAD_VALUE; }
		status_t r = addScalar(&tmp, mem.first.c_str(), mem.second, err);
		if (r != B_OK) { if (err->empty()) *err = "cannot add field \"" + mem.first + "\""; return r; }
	}
	tmp.what = out->what;
	*out = tmp;
	return B_OK;
}

static const char kB64[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
static std::string b64(const unsigned char *p, size_t n)
{
	std::string o;
	for (size_t i = 0; i < n; i += 3) {
		unsigned v = p[i] << 16 | (i + 1 < n ? p[i + 1] << 8 : 0) | (i + 2 < n ? p[i + 2] : 0);
		o += kB64[v >> 18 & 63]; o += kB64[v >> 12 & 63];
		o += i + 1 < n ? kB64[v >> 6 & 63] : '='; o += i + 2 < n ? kB64[v & 63] : '=';
	}
	return o;
}

static Json itemToJson(const BMessage &m, const char *name, type_code t, int32 i)
{
	switch (t) {
	case B_INT8_TYPE: { int8 v = 0; m.FindInt8(name, i, &v); return Json::MakeInt(v); }
	case B_INT16_TYPE: { int16 v = 0; m.FindInt16(name, i, &v); return Json::MakeInt(v); }
	case B_INT32_TYPE: { int32 v = 0; m.FindInt32(name, i, &v); return Json::MakeInt(v); }
	case B_INT64_TYPE: { int64 v = 0; m.FindInt64(name, i, &v); return Json::MakeInt(v); }
	case B_BOOL_TYPE: { bool v = false; m.FindBool(name, i, &v); return Json::MakeBool(v); }
	case B_FLOAT_TYPE: { float v = 0; m.FindFloat(name, i, &v); return Json::MakeDouble(v); }
	case B_DOUBLE_TYPE: { double v = 0; m.FindDouble(name, i, &v); return Json::MakeDouble(v); }
	case B_STRING_TYPE: { const char *v = ""; m.FindString(name, i, &v); return Json::MakeString(v); }
	case B_MESSAGE_TYPE: { BMessage v; m.FindMessage(name, i, &v); return BusMessageToJson(v); }
	default: {                                          // raw/unknown: a tagged base64 object
		const void *d = NULL; ssize_t n = 0; m.FindData(name, t, i, &d, &n);
		Json o = Json::MakeObject(), raw = Json::MakeObject();
		char code[5] = { (char)(t >> 24), (char)(t >> 16), (char)(t >> 8), (char)t, 0 };
		raw.set("type", Json::MakeString(code));
		raw.set("base64", Json::MakeString(d ? b64((const unsigned char *)d, n) : ""));
		o.set("$raw", raw);
		return o; }
	}
}

Json BusMessageToJson(const BMessage &m)
{
	Json out = Json::MakeObject();
	std::vector<std::string> arrays;
	const char *an;
	for (int32 i = 0; m.FindString(kArrays, i, &an) == B_OK; i++) arrays.push_back(an);
	char *name; type_code type; int32 count;
	for (int32 i = 0; m.GetInfo(B_ANY_TYPE, i, &name, &type, &count) == B_OK; i++) {
		if (strncmp(name, "bus:", 4) == 0) continue;     // metadata never leaves the process
		bool arr = count > 1;
		for (auto &a : arrays) if (a == name) arr = true;
		if (!arr) { out.set(name, itemToJson(m, name, type, 0)); continue; }
		Json a = Json::MakeArray();
		for (int32 k = 0; k < count; k++) a.push(itemToJson(m, name, type, k));
		out.set(name, a);
	}
	return out;
}
