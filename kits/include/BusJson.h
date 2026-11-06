/* A compact JSON value, reader and writer: just what the bus envelope needs.
   Integers stay integers (int64); a number with '.', 'e' or beyond int64 is a double.
   Objects keep insertion order. Parse depth is capped (64) so hostile input cannot
   overflow the stack. Strings are UTF-8; \uXXXX escapes (with surrogate pairs) decode. */
#ifndef _BUS_JSON_H
#define _BUS_JSON_H
#include <stdint.h>
#include <string>
#include <utility>
#include <vector>

class Json {
public:
	enum Type { Null, Bool, Int, Double, String, Array, Object };
	Json() : fType(Null) {}
	static Json MakeBool(bool b) { Json j; j.fType = Bool; j.fInt = b; return j; }
	static Json MakeInt(int64_t v) { Json j; j.fType = Int; j.fInt = v; return j; }
	static Json MakeDouble(double v) { Json j; j.fType = Double; j.fDbl = v; return j; }
	static Json MakeString(const std::string &s) { Json j; j.fType = String; j.fStr = s; return j; }
	static Json MakeArray() { Json j; j.fType = Array; return j; }
	static Json MakeObject() { Json j; j.fType = Object; return j; }

	Type type() const { return fType; }
	bool isNull() const { return fType == Null; }
	bool isObject() const { return fType == Object; }
	bool isArray() const { return fType == Array; }
	bool asBool() const { return fInt != 0; }
	int64_t asInt() const { return fType == Double ? (int64_t)fDbl : fInt; }
	double asDouble() const { return fType == Double ? fDbl : (double)fInt; }
	const std::string &asString() const { return fStr; }

	/* Array */
	void push(const Json &v) { fItems.push_back(v); }
	const std::vector<Json> &items() const { return fItems; }
	/* Object: set replaces an existing key. get returns NULL when absent. */
	void set(const std::string &key, const Json &v);
	const Json *get(const std::string &key) const;
	const std::vector<std::pair<std::string, Json> > &members() const { return fMembers; }
	std::string getString(const std::string &key, const std::string &def = "") const;

	std::string Dump() const;
	/* Parses exactly one value (trailing whitespace allowed). On failure returns false
	   and describes why in *err. */
	static bool Parse(const std::string &text, Json *out, std::string *err = NULL);

private:
	friend class JsonParser;
	void dump(std::string &out) const;
	Type fType;
	int64_t fInt = 0;
	double fDbl = 0;
	std::string fStr;
	std::vector<Json> fItems;
	std::vector<std::pair<std::string, Json> > fMembers;
};
#endif
