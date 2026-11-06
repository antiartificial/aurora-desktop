#include <BusJson.h>
#include <ctype.h>
#include <errno.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

void Json::set(const std::string &key, const Json &v)
{
	for (auto &m : fMembers) if (m.first == key) { m.second = v; return; }
	fMembers.push_back(std::make_pair(key, v));
}

const Json *Json::get(const std::string &key) const
{
	for (auto &m : fMembers) if (m.first == key) return &m.second;
	return NULL;
}

std::string Json::getString(const std::string &key, const std::string &def) const
{
	const Json *j = get(key);
	return j && j->fType == String ? j->fStr : def;
}

static void dumpString(const std::string &s, std::string &out)
{
	out += '"';
	for (unsigned char c : s) {
		switch (c) {
		case '"': out += "\\\""; break;
		case '\\': out += "\\\\"; break;
		case '\n': out += "\\n"; break;
		case '\r': out += "\\r"; break;
		case '\t': out += "\\t"; break;
		default:
			if (c < 0x20) { char b[8]; snprintf(b, sizeof b, "\\u%04x", c); out += b; }
			else out += (char)c;
		}
	}
	out += '"';
}

void Json::dump(std::string &out) const
{
	char b[40];
	switch (fType) {
	case Null: out += "null"; break;
	case Bool: out += fInt ? "true" : "false"; break;
	case Int: snprintf(b, sizeof b, "%lld", (long long)fInt); out += b; break;
	case Double:
		if (!std::isfinite(fDbl)) { out += "null"; break; }   // JSON has no NaN or Inf
		snprintf(b, sizeof b, "%.17g", fDbl);
		out += b;
		if (!strpbrk(b, ".eE")) out += ".0";                  // stay a double on re-parse
		break;
	case String: dumpString(fStr, out); break;
	case Array:
		out += '[';
		for (size_t i = 0; i < fItems.size(); i++) { if (i) out += ','; fItems[i].dump(out); }
		out += ']';
		break;
	case Object:
		out += '{';
		for (size_t i = 0; i < fMembers.size(); i++) {
			if (i) out += ',';
			dumpString(fMembers[i].first, out); out += ':'; fMembers[i].second.dump(out);
		}
		out += '}';
		break;
	}
}

std::string Json::Dump() const { std::string s; dump(s); return s; }

class JsonParser {
public:
	JsonParser(const std::string &t) : s(t), p(0) {}
	bool run(Json *out, std::string *err)
	{
		bool ok = value(*out, 0);
		if (ok) { ws(); if (p != s.size()) ok = fail("trailing data"); }
		if (!ok && err) *err = msg + " at offset " + std::to_string(p);
		return ok;
	}
private:
	const std::string &s; size_t p; std::string msg;
	bool fail(const char *m) { if (msg.empty()) msg = m; return false; }
	void ws() { while (p < s.size() && strchr(" \t\r\n", s[p])) p++; }
	bool lit(const char *w) { size_t n = strlen(w); if (s.compare(p, n, w) != 0) return false; p += n; return true; }

	bool value(Json &j, int depth)
	{
		if (depth > 64) return fail("nesting too deep");
		ws();
		if (p >= s.size()) return fail("unexpected end");
		char c = s[p];
		if (c == '{') return object(j, depth);
		if (c == '[') return array(j, depth);
		if (c == '"') { j.fType = Json::String; return str(j.fStr); }
		if (c == 't') { if (!lit("true")) return fail("bad literal"); j = Json::MakeBool(true); return true; }
		if (c == 'f') { if (!lit("false")) return fail("bad literal"); j = Json::MakeBool(false); return true; }
		if (c == 'n') { if (!lit("null")) return fail("bad literal"); j = Json(); return true; }
		return number(j);
	}

	bool number(Json &j)
	{
		size_t b = p; bool flt = false;
		if (p < s.size() && s[p] == '-') p++;
		size_t d0 = p;
		while (p < s.size() && isdigit((unsigned char)s[p])) p++;
		if (p == d0) return fail("bad value");
		if (s[d0] == '0' && p - d0 > 1) return fail("leading zero");
		if (p < s.size() && s[p] == '.') {
			flt = true; p++; size_t f0 = p;
			while (p < s.size() && isdigit((unsigned char)s[p])) p++;
			if (p == f0) return fail("bad fraction");
		}
		if (p < s.size() && (s[p] == 'e' || s[p] == 'E')) {
			flt = true; p++;
			if (p < s.size() && (s[p] == '+' || s[p] == '-')) p++;
			size_t e0 = p;
			while (p < s.size() && isdigit((unsigned char)s[p])) p++;
			if (p == e0) return fail("bad exponent");
		}
		std::string t = s.substr(b, p - b);
		if (!flt) {
			errno = 0; char *end; long long v = strtoll(t.c_str(), &end, 10);
			if (errno == 0) { j = Json::MakeInt(v); return true; }
		}
		j = Json::MakeDouble(strtod(t.c_str(), NULL));   // too big for int64: a double (lossy)
		return true;
	}

	static void utf8(std::string &o, unsigned cp)
	{
		if (cp < 0x80) o += (char)cp;
		else if (cp < 0x800) { o += (char)(0xC0 | cp >> 6); o += (char)(0x80 | (cp & 63)); }
		else if (cp < 0x10000) { o += (char)(0xE0 | cp >> 12); o += (char)(0x80 | (cp >> 6 & 63)); o += (char)(0x80 | (cp & 63)); }
		else { o += (char)(0xF0 | cp >> 18); o += (char)(0x80 | (cp >> 12 & 63)); o += (char)(0x80 | (cp >> 6 & 63)); o += (char)(0x80 | (cp & 63)); }
	}
	bool hex4(unsigned *v)
	{
		if (p + 4 > s.size()) return false;
		*v = 0;
		for (int i = 0; i < 4; i++) {
			char c = s[p++]; *v <<= 4;
			if (c >= '0' && c <= '9') *v |= c - '0';
			else if (c >= 'a' && c <= 'f') *v |= c - 'a' + 10;
			else if (c >= 'A' && c <= 'F') *v |= c - 'A' + 10;
			else return false;
		}
		return true;
	}

	bool str(std::string &o)
	{
		o.clear(); p++;                                        // opening quote
		while (p < s.size()) {
			unsigned char c = s[p++];
			if (c == '"') return true;
			if (c < 0x20) return fail("control character in string");
			if (c != '\\') { o += (char)c; continue; }
			if (p >= s.size()) break;
			char e = s[p++];
			switch (e) {
			case '"': o += '"'; break; case '\\': o += '\\'; break; case '/': o += '/'; break;
			case 'b': o += '\b'; break; case 'f': o += '\f'; break; case 'n': o += '\n'; break;
			case 'r': o += '\r'; break; case 't': o += '\t'; break;
			case 'u': {
				unsigned cp;
				if (!hex4(&cp)) return fail("bad \\u escape");
				if (cp >= 0xD800 && cp < 0xDC00) {
					unsigned lo;
					if (s.compare(p, 2, "\\u") != 0) return fail("lone surrogate");
					p += 2;
					if (!hex4(&lo) || lo < 0xDC00 || lo > 0xDFFF) return fail("bad surrogate pair");
					cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00);
				} else if (cp >= 0xDC00 && cp < 0xE000) return fail("lone surrogate");
				utf8(o, cp);
				break; }
			default: return fail("bad escape");
			}
		}
		return fail("unterminated string");
	}

	bool array(Json &j, int depth)
	{
		j = Json::MakeArray(); p++; ws();
		if (p < s.size() && s[p] == ']') { p++; return true; }
		for (;;) {
			Json v;
			if (!value(v, depth + 1)) return false;
			j.push(v); ws();
			if (p < s.size() && s[p] == ',') { p++; continue; }
			if (p < s.size() && s[p] == ']') { p++; return true; }
			return fail("expected , or ]");
		}
	}

	bool object(Json &j, int depth)
	{
		j = Json::MakeObject(); p++; ws();
		if (p < s.size() && s[p] == '}') { p++; return true; }
		for (;;) {
			ws();
			if (p >= s.size() || s[p] != '"') return fail("expected key");
			std::string k; Json v;
			if (!str(k)) return false;
			ws();
			if (p >= s.size() || s[p] != ':') return fail("expected :");
			p++;
			if (!value(v, depth + 1)) return false;
			j.set(k, v); ws();
			if (p < s.size() && s[p] == ',') { p++; continue; }
			if (p < s.size() && s[p] == '}') { p++; return true; }
			return fail("expected , or }");
		}
	}
};

bool Json::Parse(const std::string &text, Json *out, std::string *err)
{
	JsonParser jp(text);
	Json tmp;
	if (!jp.run(&tmp, err)) return false;
	*out = tmp;
	return true;
}
